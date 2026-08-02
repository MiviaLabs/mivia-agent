# 52 - Dual-surface session history: operator transcript vs model context

**Status:** DESIGN — ADLC Step 0 has **not** been run. Not implementation-ready.
**Date:** 2026-08-02
**Depends on:** `49` (compaction elision, shipped) for the current single-history
behavior this plan deliberately splits. Coordinates with program `51` (harness
context economics) and does **not** wait for it. Coordinates with any future
historical-checkpoint reader; that reader is a different product surface.
**Blocks:** nothing today. Blocks any half-split that mutates only one of
display or model history without naming the other as SoT.
**Blast radius:** HIGH — session truth, TUI/hydration, durable checkpoints,
resume/fork, privacy of retained tool bodies, and every path that currently
treats `Session.Messages` as both UI and prompt.

## 1. Goal

Give operators a **stable, human-readable session transcript** that keeps what
they already saw (user text, assistant prose, tool results as rendered when
they ran), while the **model/agent prompt** continues to use the compacted and
elided history that controls cost.

Today those are the **same** slice: after compaction or elision,
`Session.Messages`, the durable active checkpoint, and the next turn's planner
input all shrink together. Users cannot scroll back to a full prior tool body
in session history even though they may have watched it stream live.

This plan separates two surfaces:

| Surface | Audience | Compaction / elision |
|---------|----------|----------------------|
| **Operator transcript** | Human (TUI, `/status`, export, resume display) | Does **not** rewrite prior tool bodies or drop turns the human already saw |
| **Model context** | Planner + provider request | Keeps structural compaction + plan `49` elision |

One sentence: **humans keep the diary; the model gets the brief.**

## 2. Verified baseline (re-check before Step 0)

Facts as of plan `49` closeout on master (`a9cc173` / `8ff67ae` era). Re-read
code before locking implementation.

- `Session.Messages` is the single in-memory conversation SoT under `mu`
  (`internal/chat/session.go`). TUI must use `MessagesCopy()`.
- Agent turns install preparation into `loop.Messages`, then on success write
  `s.Messages = cloneContextMessages(loop.Messages)` and commit active context
  from that set (`finishAgentTurn` / `BuildCommitRequest` with `result.Active`).
- Structural compaction and tool-result elision run inside `contextmgr.Plan`
  and only affect the messages the preparation path returns
  (`internal/contextmgr/planner.go`, `planner_elision.go`).
- Live UI already has a second, partial channel: event/tool blocks and banners
  (`internal/cli` `ChatBlock`s). That channel is **not** a durable dual history;
  restart/hydrate still rebuilds from session/checkpoint messages.
- Durable store keeps append-only complete checkpoint rows and a single active
  pointer (`context_checkpoints`). Product `Load` returns only the active
  checkpoint. Plan `49` deliberately did not add a historical reader.
- Source payloads without a redaction policy are metadata-only and are not a
  full-body recovery path for the UI.

## 3. Problem statement

### 3.1 Product mismatch

Operators expect chat products to retain **what they saw**. Compaction is a
**model-cost** control. Collapsing both into one history makes cost control
look like data loss: “the tool output was there a minute ago; now the session
only has a host notice.”

### 3.2 What plan 49 correctly did not do

Plan `49` optimized the **prompt**. It left irreversible product history as an
explicit residual. Dual-surface history is the product answer to that residual
without reopening same-turn elision, admission widening, or model summarizers.

### 3.3 What this is not

- Not a public historical-checkpoint browser for arbitrary past actives
  (authz/redaction product of its own; may later feed transcript rebuild).
- Not model-facing “recall the full tool body” by default (that re-expands
  prompt cost and needs a deliberate tool or expansion policy).
- Not dual storage of every provider wire dump or raw prompt.

## 4. Design

### 4.1 Two named message lanes

Introduce an explicit split at the **session** boundary (not inside pure
`Plan`):

```text
OperatorTranscript  // append-only for completed turns; display / export / hydrate UI
ModelContext        // planner input; may compact and elide
```

**Naming is locked to intent, not to file names.** Implementation may use
`Session.Transcript` + `Session.Messages` (model), or
`DisplayMessages` + `PromptMessages`. The plan requires:

1. Exactly one lane feeds `contextmgr.Plan` / `PreparationManager.Prepare`.
2. Exactly one lane feeds human scrollback and session export meant for humans.
3. Neither lane is updated “by accident” when the other is rewritten.

### 4.2 Write rules

| Event | Operator transcript | Model context |
|-------|---------------------|---------------|
| User message accepted | Append full user text | Append same (or pointer to same immutable record) |
| Assistant final text | Append as shown | Append as sent/received for the turn |
| Tool result completed | Append **full body as committed to the turn UI** (subject to redaction policy) | Append full body initially (same as today) |
| Structural compaction | **No drop** of already-visible turns for display | Retain/elide as today |
| Plan `49` elision | **Keep original tool body** on transcript | Replace eligible prior tool `Content` with host notice |
| `/clear` / tombstone | Clear or tombstone both under the same session policy | Same |
| Failed turn | Do not publish partial transcript rows that the model never committed; match existing failed-turn discard rules | Same |

Live streaming may still paint blocks before commit; durable dual-lane updates
happen on the same success boundaries as today's `finishAgentTurn` / plain
commit.

### 4.3 Identity and pairing

Tool call IDs, roles, and pairing must remain valid on the **model** lane
(unchanged invariant). The operator lane may store the same IDs for alignment
(so a UI can mark “this tool row was elided for the model”) without requiring
the model lane to keep the full body.

Optional display annotation (not model-visible unless separately designed):

```text
(host) full tool result retained for you; model context holds an elision notice
```

Do not put that annotation into provider messages.

### 4.4 Durability

**Minimum durable requirement for v1:**

- Model context continues to use the existing active-checkpoint / preparation
  commit path (no behavior regression on prompt bytes).
- Operator transcript is durable across process restart for the same session
  principal, or the plan must explicitly say “transcript is process-local only”
  (rejected for v1 — see closed decisions).

**Preferred storage shape (open until Step 0, recommendation below):**

| Option | Idea | Pros | Cons |
|--------|------|------|------|
| **A. Parallel blob on session** | New durable field/table `session_transcript` (canonical messages for display) | Clear SoT; independent of active compact pointer | New schema + migration story |
| **B. Event-sourced display** | Rebuild transcript only from source events + payloads | Reuses store | Payloads often metadata-only; not enough without redaction policy and full payload retention |
| **C. Keep full bodies only in prior checkpoints** | UI walks checkpoint history | Reuses rows | No product API; privacy; not a coherent “chat” order without more work |

**Recommendation for challenge:** **A** for v1 — an append-oriented transcript
document owned by the session, written on successful turns, never rewritten by
`Plan`. Model lane stays on active checkpoint. Do not overload prior
checkpoints as the human chat log.

Redaction: transcript persistence must use the **same** process redaction
policy as other durable context content (INV-SEC-4 / INV-AG-32 family). Fail
open only where existing policy fails open; never invent a second policy list.

### 4.5 Resume, fork, export

| Operation | Behavior |
|-----------|----------|
| Resume session | Load transcript for UI; load active model context for the next agent turn |
| Fork | Copy both lanes or document that fork is model-context-only (must not silently drop transcript) |
| Export for humans | Prefer transcript |
| Export for “debug model view” | Optional explicit model-context dump behind a debug flag; default off |

### 4.6 TUI / classic CLI

- Scrollback and block hydration for past turns read **transcript**.
- “Context %” / budget displays remain based on **model** estimate (truth for cost).
- Compaction banners stay content-free aggregates (plan `49` counters).
- Do not re-render elided model notices into the human scrollback as if the user
  never saw the full tool output.

### 4.7 Subagents and nested loops

Root session owns the dual lanes. Nested/multi-step agents that do not own a
user-visible session keep a **single** model history unless a nested UI is
explicitly added later. Do not double storage for invisible loops.

## 5. Closed decisions (pre-Step-0 proposals — must be re-challenged)

### 5.1 v1 transcript is durable

Process-local-only transcript is rejected: restart would resurrect the current
“history disappeared” confusion.

### 5.2 Model does not auto-recall full transcript bodies

Expanding an elided body back into the prompt is a separate feature (tool,
`/expand`, or passive memory in program `51`). Dual-surface alone does not
re-inflate cost.

### 5.3 Elision notices remain model-lane only

Do not replace human-visible tool rows with the host elision string. Humans
keep the original (redacted) body on the transcript lane.

### 5.4 No product historical-checkpoint browser in this plan

Walking all `context_checkpoints` for ops forensics stays out of scope. If
needed later, design it against principal checks and redaction independently.

## 6. Open decisions (must close in Step 0)

1. **Storage schema:** table vs blob column vs content-addressed payload refs
   for transcript messages.
2. **Size policy:** unbounded transcript growth vs operator caps vs external
   log sink; interaction with uncapped tool results (plan `48`).
3. **Plain chat vs agent turns:** same dual-lane rules for both?
4. **Classic line mode vs TUI:** one transcript SoT for both surfaces.
5. **Migration:** existing sessions have only model/active history — treat as
   transcript=model snapshot at upgrade time, or transcript empty with a notice.
6. **Whether `Session.Messages` is renamed** or kept as the model lane for
   compatibility with a large call graph.

## 7. Invariants (draft)

- Planner input never silently includes operator-only full bodies after elision.
- Operator transcript never loses a tool body solely because `Plan` elided the
  model lane.
- Compaction events remain content-free.
- Failed turns do not partial-commit transcript in a way that diverges from
  today's discard semantics without an explicit decision.
- Redaction policy is shared; transcript is not a side channel past privacy
  config.
- Subagent-invisible loops do not invent a second user transcript.

## 8. Delivery slices (after Step 0 lock)

| Wave | Scope | RED-first gates |
|------|--------|-----------------|
| 1 | Types + session dual-lane write rules (in-memory only) | Unit: elision mutates model lane only; transcript keeps body |
| 2 | Durable transcript commit beside existing checkpoint commit | Integration: restart session → UI messages full, next prepare still elided |
| 3 | TUI/classic hydrate from transcript; budget from model lane | UI tests + `MessagesCopy` / display API split |
| 4 | Resume/fork/export + migration of single-lane sessions | Integration matrix |
| 5 | Docs (owned product path only) + invariant IDs | `make validate-invariants` / `make invariants` |

Each wave stays behind feature completeness: do not ship dual in-memory without
durable transcript if §5.1 remains closed.

## 9. Required tests (acceptance sketch)

- After multi-turn elision (reuse plan `49` fixtures): model prepare messages
  contain the elision notice; transcript still contains the original marker
  body (redaction permitting).
- Process restart: transcript marker survives; active model context still
  elided.
- Compaction event / logs still omit tool content.
- `/clear` and session delete remove or tombstone **both** lanes.
- No test introduces a product checkpoint-history API; use session transcript
  APIs only.
- Race: concurrent `MessagesCopy` / transcript copy under session `mu`.

## 10. Verification (when implemented)

```text
go test -race ./internal/chat ./internal/agent ./internal/contextmgr ./internal/cli ./internal/storage
make validate-invariants
make invariants
make verify
make build
```

Manual: multi-turn tool session with oversized prior result, confirm TUI
scrollback still shows full tool output after a later compacting turn, while
`/status` or debug model-context view shows reduced prompt estimate.

## 11. Out of scope

- Model summarizer / LLM-authored session digests.
- Same-turn elision of live tool results.
- Widening `RecentTail` admission.
- Product historical checkpoint browser / `mivia context show`.
- Default model tool to re-inject full transcript bodies into the prompt.
- Passive-only dual lanes (rejected in §5.1 unless Step 0 reopens it).
- Changing plan `49` eligibility (latest tool unit remains non-elidable on the
  model lane).

## 12. Relationship to other plans

| Plan | Relationship |
|------|----------------|
| `49` | Shipped single-lane elision; this plan **adds** the human lane without undoing model savings |
| `48` | Uncapped tool bodies make dual-surface more important (humans keep large results; model elides) |
| `51` | Economics for **model** context; transcript growth policy must not re-enter the prompt |
| `42` / `43` | Agent-requested / adaptive compaction still rewrite **model** lane only under this design |

## 13. Effort and risk (for prioritization)

| Dimension | Assessment |
|-----------|------------|
| Effort | **Medium–large** (session SoT split, durability, hydrate, migration, privacy) — larger than plan `49` elision alone |
| Risk | **High** if lanes drift or UI keeps reading model messages |
| Value | High UX honesty: cost control without “chat history amnesia” |
| Need | **Product decision** — not required for model cost; required if operators must trust scrollback after compaction |

## 14. Rollback

If dual-lane drift is detected (UI shows content the model never had in a way
that confuses support, or model lane regains full bodies), disable transcript
persistence and fall back to single-lane model history with a startup notice.
Do not “merge” lanes by re-expanding elided model bodies by default.

## 15. Step 0 challenge checklist

Hostile review must attack at least:

1. Single SoT bugs: any remaining writer that sets `Messages` from loop without
   updating transcript (or the reverse).
2. Privacy: transcript retaining secrets the active checkpoint redacted away.
3. Size: multi-day uncapped tool transcripts disk growth.
4. Resume: loading model active without transcript (or transcript without
   model) leaves a usable session.
5. Nested agents: no accidental dual storage explosion.
6. Whether TUI blocks already duplicate transcript and cause double display.

Until those are dispositioned, status stays DESIGN.
