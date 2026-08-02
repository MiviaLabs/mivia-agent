# 49 - Compaction: tool-result elision tier and usable summaries

**Status:** DESIGN - revised 2026-08-02 after reading the shipped code. Two
premises of the first draft were wrong; see §2. Needs the ADLC Step 0
challenge round before any code.
**Date:** 2026-08-02
**Depends on:** `41` (structural compaction, shipped), `48` (bounded results
make elision cheaper). Coordinates with `42` (agent-requested compaction) and
`43` (adaptive policy) without blocking them.
**Blast radius:** HIGH - compaction planner semantics, message shape
invariants, summarizer gating, durable payload retention, observability.

## 1. Problem

The always-on compaction path amputates instead of distilling.

`retainMessages` (`internal/contextmgr/planner.go:159`) keeps
{system, latest user objective, latest tool unit} plus whole units from the
tail while they fit under target and under `RecentTail` (default 8) messages.
Everything else is dropped: no summary, no stub, no pointer. The model then
re-derives lost findings with fresh tool calls - the waste shows up as
repeated work, not as a visible cost line.

Units are all-or-nothing (`messageUnits`, `planner.go:215`): one huge `grep`
result forces its entire unit out, evicting the sibling assistant reasoning
with it. There is no "keep the call, drop the body" middle tier.

## 2. What the first draft of this plan got wrong

Both corrections change the design, so they lead.

### 2.1 The summarizer is not gated - it is not wired at all

`internal/cli/context_setup.go:104` builds
`contextmgr.PreparationCommitter{Store: store}`. The `Summarizer` field is
nil, and `CommitPreparation` summarizes only
`if preparation.Compacted && request.Summarizer != nil`
(`internal/contextmgr/commit.go:29`). **No configuration produces a summary in
the shipped binary.** The five-condition gate in `Summarizer.available`
(`summarizer.go:75`) is real but unreachable.

Consequence: "relax the gating" is not the first move. Wiring a
`SummaryProvider` is - and that is a larger decision than it looks, because it
means the compaction path makes a provider call with conversation-derived
input on a schedule the user did not ask for. That decision deserves the same
scrutiny the gate was written to enforce. This plan therefore treats summaries
as **step 3**, behind an explicit opt-in, and does not claim default-install
summarization.

### 2.2 The dropped bodies are frequently NOT retained

The first draft asserted the content survives in
`context_source_events`/`context_payloads`. That holds only when a redaction
policy is configured. `SanitizeSourcePayload`
(`internal/contextstate/sanitize.go:64`) returns `HashOnly` - ref, size and
digest, **no bytes** - whenever `policyConfigured(policy)` is false, and
`contextRedactionPolicy` (`internal/cli/context_setup.go:55`) yields the zero
policy unless the workspace sets `[privacy] redaction_patterns` or
`redaction_key_names`, which have no defaults.

So on a default install there is nothing to page back in. This does not sink
the elision tier - a stub that keeps the tool call and the sibling reasoning
is strictly better than evicting the unit, and the body was being dropped
either way - but it does mean:

- the stub must not promise recall it cannot deliver; it states recoverability
  from the payload's actual retention, not from hope;
- the recall tool (§4.2) is only useful on installs that retain payloads;
- "retain compaction-elided tool bodies regardless of redaction policy" is a
  **security decision**, not a performance one (it writes raw tool output to
  disk on installs that today write none). It is an open question in §9, not
  an assumption.

### 2.3 Stubs cannot carry a source event id today

The draft's stub format embeds `source_event:<id>`. The planner has
`PlanInput.SourceEvents`, but no mapping from a message index to the event
holding its body: `ProjectSource` numbers events per *turn* over that turn's
messages while `PlanInput.Messages` is the whole active context across many
turns, and it skips system messages, so positions do not correspond.
Threading an index→SourceID map through `PrepareInput`/`PlanInput` is possible
but touches the preparation contract.

Resolution: the stub is **content-addressed**. `newContentRef`
(`sanitize.go:137`) already derives the payload key from `sha256(bytes)`, so
`sha256:<12-hex>` in the stub is a lookup key against `context_payloads` with
no new plumbing. An event id can be added later without a format change.

## 3. Goal

Three ordered, independently shippable mitigations:

1. **Elision tier** (deterministic, no model call, no new gates): before
   evicting whole units, replace large tool-result bodies with a compact stub.
2. **Loud degradation**: compaction stops silently discarding things - both
   elided bodies and (once summaries exist) discarded summary content.
3. **Opt-in summarization that actually runs**: wire a summarizer at all,
   behind explicit configuration, and only then consider graduated gating.

## 4. Design

### 4.1 Elision tier (core of this plan)

A pass in `retainMessages` between the mandatory set and tail selection:

- Candidates: tool-result messages (`RoleTool`) outside the mandatory set
  whose `Content` exceeds `elide_threshold_bytes` (default 2048; 0 disables),
  oldest first.
- Replacement stub, deterministic and <= 256 bytes:

  ```
  [elided tool result: <tool_name>, <N> bytes, sha256:<12-hex><, recoverable>]
  <first line, bounded to 120 bytes, redaction-policy-passed>
  ```

  `recoverable` appears only when the payload for that content is actually
  dereferenceable on this install. The excerpt line is omitted entirely when
  no redaction policy is configured (id + size + hash only).
- The assistant tool-call message of the unit is **kept verbatim**: the model
  retains what it asked for and what it concluded. Pairing stays valid because
  the stub *is* the tool result message - same role, same `ToolCallID`, new
  content - so `validateMessageShape` passes unchanged.
- Unit eviction proceeds only if the target is still not met after full
  elision. Expected effect: most compactions stop evicting units at all, since
  tool bodies dominate cost.
- The planner stays a pure function. It cannot query storage, so
  "recoverable" must arrive as data: `PlanInput` gains
  `RetainedContent map[string]bool` (sha256 → dereferenceable), populated by
  the preparation layer and empty on installs that retain nothing. Empty map =
  every stub prints as unrecoverable, which is the honest default.

### 4.2 Recall path

- **Step 1 (this plan):** the host can show an elided body -
  `mivia context show <sha256>` - on installs that retain payloads.
- **Step 2 (separate plan, aligns with `42`'s tool-surface rules):** a
  read-only `recall` tool over `SourceReader.ReadRange`/`ReadPayload` with
  `ledger_read`-style pagination and a per-turn budget. Out of scope here; the
  stub format needs no change for it.

### 4.3 Loud degradation

- `EmitCompaction` (`internal/agent/emit.go:79`) reports what happened: elided
  message count, bytes reclaimed by elision, units evicted, and summary status
  (`none | not-configured | structural-only | full`). `CompactionEvent` is a
  sealed constructor type, so this is a schema change with a validator update,
  not a struct-literal edit.
- Once summaries exist: discarding a summary body for want of a redaction
  policy emits a typed warning and a once-per-session operator log rather than
  writing `redaction_status: "structural-only"` in silence (`summary.go:195`).
- A compaction that evicts units *after* full elision says so - that is the
  case where work is genuinely lost.

### 4.4 Summaries (step 3, opt-in only)

Wire a `Summarizer` into `PreparationCommitter` only when the workspace
explicitly enables it (`[context] summary = true` plus the existing policy
snapshot fields). Ship it off by default. Revisit the graduated-gating table
from the first draft **after** there is telemetry from installs that opted in;
the "summarize elision stubs instead of raw bodies" tier is attractive
precisely because stubs carry no payload, but it needs security review, and
proposing it before a summarizer runs at all is premature.

## 5. Invariants

- Planner remains a pure function; no storage, network, or clock in `Plan`.
- Tool pairing and message shape valid after every pass.
- Mandatory set (system, current objective, latest tool unit) is never elided
  or evicted; the latest tool unit keeps its full body.
- Idempotency key accounts for elision: identical inputs produce identical
  stubs and therefore an identical key (stubs are fingerprinted like any other
  message by `plannerMessages`).
- A stub never claims recoverability the install cannot honour.
- Stubs never carry secret-bearing content: the excerpt passes the configured
  redaction policy and is dropped entirely when none is configured.
- Summary failure never blocks compaction (structural fallback intact).

## 6. Implementation steps

1. `elide_threshold_bytes` config knob + `PlanInput.ElideThresholdBytes` and
   `RetainedContent`; defaults wired through `StructuralPreparationManager`.
2. Elision pass in `planner.go` + stub constructor. Planner tests: stub
   determinism, idempotency-key stability, pairing validity, target met by
   elision alone with zero evictions, threshold edges, non-UTF-8-safe excerpt
   cut, empty body, unrecoverable-by-default rendering.
3. Preparation layer populates `RetainedContent` from the payload store.
4. `CompactionEvent` schema: elided count, bytes reclaimed, evicted count,
   summary status; validator + emitter + TUI surface.
5. `mivia context show <sha256>` host command.
6. Opt-in summarizer wiring (`[context] summary`), loud discard event/log.
7. Docs: compaction lifecycle - what survives, what is stubbed, what is
   recoverable and under which configuration.

Steps 1-4 are the shippable core; 5-7 can follow separately.

## 7. Testing

- Planner: transcript with 3 large tool results at 85% budget - elision alone
  reaches target with zero units evicted; the same transcript on today's code
  evicts 2 units (the assertion that proves the tier does something).
- Idempotency: same snapshot planned twice - identical key and identical stubs.
- Recoverability rendering: same transcript with and without `RetainedContent`
  entries produces "recoverable" and bare stubs respectively.
- Excerpt suppression when no redaction policy is configured.
- Regression: `ErrPromptBudgetExceeded` still raised when even full elision
  plus eviction cannot meet budget.
- Integration (agent loop): a turn whose history exceeds trigger comes back
  with the tool call intact and its body stubbed, and the next provider request
  is valid (pairing) and under budget.

## 8. Failure analysis

- **Model treats a stub as the real output.** The stub says "elided" and gives
  size and hash; the recall path (§4.2 step 2) is the durable fix. Until then
  the failure mode is a re-run tool call - today's behaviour, not worse.
- **Elision churn.** Stubs are stable strings; once stubbed, a message's cost
  is tiny and it survives future plans. No oscillation.
- **Excerpt leaks a secret.** Suppressed entirely on unconfigured installs.
- **Recoverability map goes stale** (payload pruned between plan and recall):
  the recall command reports "no longer retained" rather than fabricating; the
  stub is a claim about plan time and says so.
- **Wiring a summarizer surprises an operator with provider calls.** Off by
  default, explicit config key, and the compaction event names the summary
  status on every compaction.

## 9. Open questions (decide before step 6)

- Should compaction-elided tool bodies be retained even when no redaction
  policy is configured, so recall works on default installs? This writes raw
  tool output at rest where today nothing is written. Security review owns
  this call; the elision tier ships either way.
- Does `RetainedContent` belong on `PlanInput`, or should the planner emit
  hash-only stubs and let the host rewrite them with recoverability at
  publication time? The former keeps one pass; the latter keeps `PlanInput`
  smaller. Resolve in the Step 0 challenge.
