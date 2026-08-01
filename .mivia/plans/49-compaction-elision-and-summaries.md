# 49 - Compaction: tool-result elision tier and usable summaries

**Status:** DESIGN
**Date:** 2026-08-02
**Depends on:** `41` (structural compaction, shipped), `48` (bounded results
make elision cheaper). Coordinates with `42` (agent-requested compaction) and
`43` (adaptive policy) without blocking them.
**Blast radius:** HIGH - compaction planner semantics, message shape
invariants, summarizer gating, durable payload retention, observability.

## 1. Problem

The always-on compaction path amputates instead of distilling:

- The structural planner (`internal/contextmgr/planner.go`) keeps only
  {system, latest objective, latest tool unit, ~8-message recent tail} and
  **drops everything else with no summary and no pointer**. The model then
  re-derives lost findings with fresh tool calls - the token waste shows up
  as repeated work, not as visible cost.
- Units are all-or-nothing: one huge `grep` result forces its entire unit out,
  evicting sibling assistant reasoning with it. There is no "keep the call,
  drop the body" middle tier.
- The summarizer exists (`summarizer.go`, `summary.go`) but requires
  SummaryEnabled && NetworkEnabled && RedactionConfigured && CredentialScope
  && endpoint-allowlist match; absent a redaction policy it **silently
  discards the summary body** and persists only a digest
  (`redaction_status: "structural-only"`). Nothing tells the operator their
  summaries are being thrown away.

The dropped content is not gone - `context_source_events`/`context_payloads`
retain full pre-compaction history behind `SourceReader.ReadRange` - it is
simply unreachable from the model and unreferenced by the compacted prompt.

## 2. Goal

Three ordered mitigations, each independently shippable:

1. **Elision tier** (deterministic, no model call): before evicting whole
   units, replace large tool-result bodies with a compact stub carrying a
   durable pointer.
2. **Loud degradation**: summary discarding stops being silent.
3. **Summaries on by default when safe**: relax the all-or-nothing gate into
   a graduated policy so default installs get *some* distillation.

## 3. Design

### 3.1 Elision tier (core of this plan)

Extend `Plan` with a pass between "mandatory set" and "unit eviction":

- Candidates: tool-result messages outside the mandatory set whose body
  exceeds `elide_threshold_bytes` (default 2048), oldest-first.
- Replacement stub (deterministic, bounded <= 256 bytes):

  ```
  [elided tool result: <tool_name>, <N> bytes, sha256:<12-hex>,
   source_event:<id>; first line: <bounded excerpt>]
  ```

- The assistant tool-call message of the unit is **kept verbatim** - the model
  retains what it asked for and what it concluded; only the raw body is
  stubbed. Tool pairing stays valid because the stub *is* the tool result
  message (same role/ids, new content) - `validateMessageShape` passes
  unchanged.
- Only if the target is still not met after full elision does unit eviction
  proceed as today. Expected effect: most compactions stop evicting units at
  all, since tool bodies dominate cost.
- Determinism/idempotency: stub content is a pure function of the source
  event; the plan's SHA idempotency key covers stubs like any message.
- Elision is planner-internal and needs none of the summarizer's gates: it
  moves no content anywhere - the body already sits in the durable payload
  store; the stub only names what was already persisted.

### 3.2 Recall path

A stub without a deref path just relocates the re-derivation waste. Two-step:

- **Step 1 (this plan):** stubs carry the source event id; the *host* CLI can
  show elided bodies (`mivia context show <event>`) for humans.
- **Step 2 (separate plan, aligns with plan `42`'s tool-surface rules):** a
  read-only `recall` tool exposing `SourceReader.ReadRange`/`ReadPayload`
  with `ledger_read`-style offset/limit pagination and a per-turn recall
  budget. Not in scope here; the stub format above is designed so that plan
  needs no format change.

### 3.3 Loud degradation

- When `CommitPreparation` runs a summary whose content is discarded
  (`redactionConfigured == false`), emit a typed warning event
  (`internal/events`) and a once-per-session operator log:
  "compaction summary discarded: no redaction policy configured".
- Surface summary status in the compaction progress event that already
  exists (`EmitCompaction`): `summary: none | structural-only | full`.

### 3.4 Graduated summary gating

Replace the single conjunctive gate with tiers:

| Condition | Behavior |
|---|---|
| Redaction configured + network + credentials | full summary (as today) |
| No redaction policy | summary call runs against the **elision-stubbed** retained view only (bodies already removed), so the input contains no raw tool payloads; persist content with `redaction_status: "stub-input"` |
| No network / no credentials / disabled | structural only (today's behavior), but loud per 3.3 |

The middle tier is the new default-install win: summarizing stubs and
assistant text is dramatically lower-risk than summarizing raw payloads, and
it reuses the sealed `SummaryEnvelope` limits unchanged (10 s timeout,
2048-token output, 12 KiB metadata). Security review must sign off on the
"stub-input" classification before this tier ships; if rejected, tiers 1 and
3 still stand and the plan degrades to 3.1-3.3.

## 4. Invariants

- Planner remains a pure function; no storage or network in `Plan`.
- Tool pairing and message shape valid after every pass (existing
  `validateMessageShape` re-check covers stubs).
- Mandatory set (system, current objective, latest tool unit) is never elided
  or evicted - the latest tool unit keeps its full body.
- Idempotency key accounts for elision (same inputs -> same stubs -> same key).
- Stubs never contain secret-bearing content: the excerpt line passes through
  the process redaction policy (`internal/redact`) if configured, and is
  dropped (id+size+hash only) if not.
- Summary failure still never blocks compaction (structural fallback intact).

## 5. Implementation steps

1. `elide_threshold_bytes` policy knob (default 2048; 0 disables elision).
2. Elision pass in `planner.go` + stub constructor with redaction-aware
   excerpt; extend planner unit tests (deterministic stubs, idempotency key,
   pairing validity, target-met-without-eviction cases).
3. `EmitCompaction` gains summary-status + elided-count fields.
4. Loud-degradation events/logs per 3.3.
5. Graduated gate in `commit.go`/`summary.go` with the `stub-input` tier
   behind a config flag pending security review.
6. `mivia context show <event>` host command for human recall.
7. Docs: compaction lifecycle page updated (what survives, what is stubbed,
   how to recover bodies).

## 6. Testing

- Planner: transcript with 3 large tool results at 85% budget -> elision
  alone reaches target, zero units evicted; verify prior behavior evicted 2
  units.
- Exactly-at-threshold body, empty body, non-UTF-8-boundary excerpt.
- Idempotency: same snapshot planned twice -> identical key and stubs.
- Summary tiers: each gate combination asserts the expected
  `redaction_status` and event emission; no-redaction install produces a
  persisted, non-empty stub-input summary (behind flag).
- Regression: `ErrPromptBudgetExceeded` still raised when even full elision +
  eviction cannot meet budget.

## 7. Failure analysis

- Model treats a stub as real output and "hallucinates" the body: stub text
  explicitly says elided and names size/hash; recall path (3.2 step 2) is the
  durable fix.
- Elision churn (same body re-elided every plan): stubs are stable strings;
  once stubbed, a message's cost is tiny and it naturally survives future
  plans - no oscillation.
- Excerpt leaks a secret on unredacted installs: excerpt suppressed entirely
  in that configuration (id+size+hash only), per invariants.
- Summary of stubs is too vague to help: acceptable - it supplements, never
  replaces, the recall path; measured via re-derivation rate in follow-up.
