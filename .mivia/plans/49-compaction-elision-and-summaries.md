# 49 - Compaction: tool-result elision tier

**Status:** DESIGN - challenge round outstanding before implementation.
**Date:** 2026-08-02
**Depends on:** `41` (structural compaction, shipped), `48` (bounded tool
results). Coordinates with `42` and `43` without blocking them.
**Blast radius:** HIGH - compaction planner semantics, durable checkpoint
contents, message shape invariants, observability.

## 1. Problem

`retainMessages` (`internal/contextmgr/planner.go:159`) keeps {system, latest
objective, latest tool unit} plus whole units from the tail under an 8-message
cap, and drops everything else with no stub and no pointer. Units are
all-or-nothing (`messageUnits`, `planner.go:215`): one huge `grep` result
evicts the sibling assistant reasoning with it. The model then re-derives lost
findings with fresh tool calls - the waste shows up as repeated work, not as a
visible cost line.

The fix is a middle tier between "keep the whole unit" and "drop it": keep the
tool call and the reasoning around it, replace only the oversized body.

## 2. Design

### 2.1 The age boundary is the safety argument

Compaction runs inside the tool loop, and its output becomes the live history:
`l.Messages = clonePreparedMessages(preparation.Messages)`
(`internal/agent/context.go:47`). End-of-turn projection reads that same array,
but it is **turn-scoped** - `contextTurnMessages` slices from the latest user
message (`internal/chat/context_integration.go:439`) and only that slice
reaches `ProjectSource` (`context_publication.go:36`).

So a tool body at or after `objectiveIndex` has not yet been persisted
anywhere: rewriting it would destroy the only copy. A tool body *before*
`objectiveIndex` was committed by its own turn - it is in that turn's source
events and in the checkpoint that recorded it - so rewriting it in place loses
nothing.

That boundary is what makes in-place elision safe, and everything else in this
plan follows from it.

A `RoleTool` message is an elision candidate iff **all** hold:

- `index < objectiveIndex` - its turn already committed;
- it is not in the mandatory set (system, objective, latest tool unit);
- `len(Content) > elideThresholdBytes` (package constant, 2048);
- the stub is strictly cheaper **in tokens** than the body it replaces
  (`estimateTokens` is `len/4` with a floor of 1, `provider/context.go:12`, so
  a byte comparison and a token comparison disagree at the margin - compare in
  tokens, the unit the selector actually uses).

This is a pure predicate, evaluated once **before** the selection pass. Every
message therefore has a fixed cost by the time selection runs, which makes the
algorithm single-pass, terminating, and independent of evaluation order. It is
not an incremental "elide until under target" loop: that would make elision
depend on selection and selection depend on elision, with no fixed point.

Candidates are rewritten in the planner's cloned output. `input.Messages` is
never mutated - `Plan` returns errors after selection (`planner.go:104,107`)
and the loop re-plans the same input on the cancellation path
(`agent/context.go:31`), so an in-place edit of the input would corrupt live
history on a failed plan.

### 2.2 One view, one set of numbers

The stub replaces the body in the retained set, so everything downstream
describes the same bytes: `PlanResult.Messages`, `Candidate.ActiveContext`,
`AfterTokens`, the idempotency fingerprint, and the request that goes on the
wire.

This matters because `Candidate.ActiveContext = MarshalCanonical(retained)`
(`planner.go:113`) is written verbatim into `context_checkpoints` on every
commit (`internal/storage/context_store.go:184`). Any design where the planner
costs one view and stores another decouples the checkpoint from the prompt
budget - and `limits.go` records the assumption that would break: a
checkpoint's "natural ceiling is the model's prompt budget, which the planner
already enforces upstream". With one view, `active_context` shrinks along with
the request, so elision reduces at-rest exposure rather than growing it.

It also means there is no work for the host to do. Splitting "compute the
elisions" from "apply them at request-build time" would scatter one invariant
across every request builder (`agent/loop.go`, `chat/context_integration.go`)
with nothing to enforce it: there is no cost check before send on the prepared
path - `promptBudgetErrorWithTools` runs only when `PreparationManager == nil`
(`agent/context.go:14`) - so a builder that skipped the step would ship an
over-budget request with no local error.

### 2.3 Admission and bounds

`RecentTail` (default 8, `planner.go:17`) keeps bounding **full-fidelity**
messages. Elided units are admitted under a second, explicit ceiling,
`maxElidedTailMessages` (constant, 64 - the existing `maxRecentTailMessages`),
and still only while cost stays under target.

Both ceilings are constants, not config knobs. A bounded count times a bounded
per-message size is what keeps `active_context` bounded; an uncapped elided
tail would put an unbounded blob into every commit, and under an operator-set
checkpoint limit (`internal/config/types.go:41`) one over-limit commit refuses
the whole turn and the context only grows from there.

Retention stays a suffix: never admit a unit older than the newest rejected
unit, so the model receives a contiguous recent history rather than a sparse
scatter of ancient stubs interleaved with recent messages.

`EstimateRequestCost` re-marshals every registered `ToolSpec` on every call
(`provider/context.go:75`), and the widened scan calls it more often than the
8-message loop did, so tool-schema cost is hoisted out of the selection loop
and selections are costed incrementally.

### 2.4 Stub text

```
[elided tool result: <tool_name>, <N> bytes]
```

`tool_name` is **untrusted input**. It is copied verbatim from the model's own
tool call - `Name: r.toolCall.Function.Name` (`internal/agent/loop_tools.go:49`)
- and nothing bounds it: `validateToolCall` requires only non-empty
(`provider/context.go:181`), and a rejected call's error body reaches history
through `failToolTask` without `capToolResult` (`loop_tools.go:374`). Wrapping
that string in system-authored framing and re-sending it every turn is a
forged-boundary risk.

So the renderer resolves the name against the tool registry: a name that does
not resolve renders as `unknown`, and a resolved name is bounded to 64 bytes
and restricted to `[A-Za-z0-9_.-]`, replaced wholesale with `unknown` if it
does not match - never stripped in place, since stripping preserves
attacker-chosen substrings.

No digest, no body excerpt, no identifier in the stub:

- A content digest would be the first path sending a hash of tool output to a
  third-party provider, and a truncated one is a confirmation oracle for a
  guessed body.
- An excerpt is the only part that can carry a secret, and the planner has no
  redaction policy with which to classify one. `PlanInput` deliberately carries
  none, and `Summarize` strips policy from anything provider-facing
  (`summarizer.go:47`). A byte-bounded excerpt would also split UTF-8, which
  `SanitizeSourcePayload` rejects outright (`contextstate/sanitize.go:48`) -
  failing a completed turn.
- An identifier would be useless anyway: payload rows are keyed by
  `contentRefID(principal, digest)` (`sanitize.go:164`), the digest is over the
  post-redaction bytes (`sanitize.go:61,137`), and `ReadPayload` requires a
  complete owner-scoped `ContentRef` (`storage/context_source.go:107`). Recall
  needs a real ref, which is the follow-on plan's job.

Exact `<N> bytes` is safe to state: the provider already received the full body
earlier in the same run under the same fenced binding, so the length discloses
nothing new, and the size is what tells the model whether re-running the tool
is worth it.

## 3. Scope limits

Stated so nobody reads a guarantee that is not here:

- **A single oversize newest tool result still returns
  `ErrPromptBudgetExceeded`.** It sits at or after the objective, so it is not
  a candidate. Eliding it would mean rewriting a body before its turn commits,
  which needs its own safety argument and its own plan.
- **The current turn's tool traffic is never elided**, so a turn that outgrows
  the budget on its own is unaffected.
- **The idempotency key changes** for transcripts that elide: it fingerprints
  the retained set (`planner.go:322,359`), which now contains stubs. That is a
  correct consequence of changing the algorithm. An in-flight commit across a
  binary upgrade will miss replay detection and surface `ErrStaleRevision` on
  retry - release-note it.
- **Resume rehydrates stubs, not bodies**, because `active_context` holds the
  stubs. The full body remains in the source events and checkpoint of the turn
  that produced it.

## 4. Invariants

- `Plan` is a pure function of its inputs: no storage, network or clock, and
  `input.Messages` is never mutated.
- No message at or after `objectiveIndex` is ever elided, so no body is
  rewritten before the turn that produced it has committed.
- `PlanResult.Messages`, `Candidate.ActiveContext`, `AfterTokens` and the
  outbound request all describe the same bytes.
- Retained message count is bounded by `RecentTail + maxElidedTailMessages`;
  retained bytes by that count times the stub bound plus the full-fidelity
  tail.
- Elision never increases the request's cost in tokens.
- Tool pairing stays valid: elision rewrites content only, leaving role and
  `ToolCallID` untouched, and selection stays unit-granular (`unitSelected`,
  `planner.go:254`), so a stub's paired assistant call can never be dropped
  while the stub is kept.
- Stub text contains no model-authored bytes that are not registry-resolved.
- `ErrPromptBudgetExceeded` is still raised when elision plus eviction cannot
  meet budget.

## 5. Waves

- **W1** elision predicate + stub renderer (registry-resolved, bounded name),
  RED tests first: age boundary, threshold edges, token-comparison skip,
  hostile tool names, mandatory-set exclusion.
- **W2** planner integration: predicate before selection,
  `maxElidedTailMessages` admission, suffix policy, incremental costing. **The
  agent-loop integration test lands in this wave**, not later - the durable
  behaviour is the risk, so it is tested alongside the change that could break
  it.
- **W3** counters (`ElidedMessages`, `ElidedBytes`, `EvictedUnits`) plumbed
  through `Preparation` to the compaction event. `NewCompactionEvent` takes a
  validated params struct rather than growing to nine positional arguments, and
  its validator must accept legal zero values: `EmitCompaction` swallows the
  constructor error (`agent/emit.go:83`), so a too-strict rule deletes the
  event instead of reporting it. A compaction that still evicts units after
  elision says so - that is where work is genuinely lost.
- **W4** TUI surface + docs: what compaction keeps, what it stubs, and where an
  elided body still exists.

## 6. Testing

- Age boundary: a transcript whose only oversize tool results sit at or after
  the objective elides nothing; move the objective and the same results elide.
- Durability (integration): after a turn whose earlier history was elided, this
  turn's projected source events and committed `active_context` carry the
  current turn's full bodies, and the previous turn's checkpoint still carries
  the elided ones.
- Admission: a 60-message transcript retains more than 8 messages when the
  extras are elided, never more than `RecentTail + maxElidedTailMessages`, and
  the retained byte count stays bounded.
- Suffix policy: no unit is admitted that is older than the newest rejected
  unit.
- Determinism: the same input planned twice yields identical retained sets,
  stubs and keys.
- Token invariant: a body whose stub would cost more in tokens is not elided.
- Hostile name: a tool call named with 1 KiB of control characters renders as
  `unknown`, and the stub stays under its bound.
- `ErrPromptBudgetExceeded` is still returned for a single oversize newest
  result.
- Loop integration: the outbound request is valid under `ValidateToolPairing`
  and its cost equals `AfterTokens`.

## 7. Failure analysis

- **Model treats a stub as real output.** The stub says "elided" and gives the
  size; the worst case is that it re-runs the tool, which is today's behaviour.
- **A body is elided before its turn commits.** The failure that would matter
  most, prevented by the age boundary and pinned by the durability integration
  test.
- **The elided tail grows.** Hard-capped by `maxElidedTailMessages`, a constant
  rather than a knob precisely so it cannot be raised into a wedge.
- **Elision saves too little to matter.** Measure elidable bytes on a real long
  session before W2; if the age boundary leaves little to elide, close the plan
  and take the token cost rather than ship machinery that saves nothing. This
  is the rollback criterion.

## 8. Scorecard (self-assessed)

| Criterion | Status |
|---|---|
| Compiles / no import cycles | PASS |
| No breaking public API (internal packages only) | PASS |
| Planner stays pure and testable in isolation | PASS |
| No new config knob | PASS |
| Durable behaviour: same-turn bodies preserved | PASS |
| Retained set bounded in count and bytes | PASS |
| Every new function has a preceding test task | PASS |
| Challenge round | **NOT RUN - gate open** |

## 9. Out of scope

- **A `recall` tool** over `SourceReader.ReadRange`/`ReadPayload` with
  pagination and a per-turn budget, threading a real `ContentRef`. The stub
  format above needs no change for it.
- **Summarization.** No summarizer is wired at all today:
  `cli/context_setup.go:104` builds `PreparationCommitter{Store: store}` with a
  nil `Summarizer`, and `CommitPreparation` summarizes only when it is non-nil
  (`commit.go:29`), so the policy gate in `Summarizer.available`
  (`summarizer.go:75`) is unreachable. Wiring one means making provider calls
  on a schedule the user did not ask for; that is a separate decision with its
  own security surface.
- **Two bugs this design work surfaced, neither fixed here:**
  `failToolTask` writes a tool error into history without `capToolResult`
  (`loop_tools.go:374`), an unbounded write with a model-chosen name; and
  `StructuralPreparationManager.RecentTail`/`OutputReserve` are dead fields
  nothing reads (`structural.go:18,39`).
