# 49 - Compaction: tool-result elision tier

**Status:** DESIGN v4 - rewritten 2026-08-02 after TWO ADLC Step 0 challenge
rounds, both returning RETURN-TO-DESIGN from all three reviewers. §2 records
what each round proved. v4 is a materially different design again and needs a
third round before lock.
**Date:** 2026-08-02
**Depends on:** `41` (structural compaction, shipped), `48` (bounded results).
**Blast radius:** HIGH - compaction planner semantics, durable checkpoint
contents, message shape invariants, observability.

## 1. Problem

`retainMessages` (`internal/contextmgr/planner.go:159`) keeps {system, latest
objective, latest tool unit} plus whole units from the tail under an
8-message cap, and drops everything else with no stub and no pointer. Units
are all-or-nothing (`messageUnits`, `planner.go:215`): one huge `grep` result
evicts the sibling assistant reasoning with it. The model re-derives lost
findings with fresh tool calls.

## 2. Challenge history - what two rounds proved

### Round 1 killed in-planner rewriting of ALL messages (v2)

**Verified:** `Plan`'s output is written back over live history -
`l.Messages = clonePreparedMessages(preparation.Messages)`
(`internal/agent/context.go:47`) - and end-of-turn projection reads that same
array (`internal/chat/session.go:346` → `ProjectSource`). A planner-written
stub would replace the durable copy of **same-turn** tool results, which today
survive to commit.

**Verified:** stubbing alone saves nothing, because `retainMessages` admits
tail units under a MESSAGE COUNT cap and returns only selected indices
(`planner.go:189,206`) - a stubbed but unselected message is discarded with
its stub.

**Verified:** the recoverability design addressed a store that does not work
that way - `newContentRef` hashes post-redaction bytes (`sanitize.go:61,137`),
the row key is principal-scoped (`sanitize.go:164`), and `ReadPayload` needs a
complete `ContentRef` (`storage/context_source.go:107`). Digest, excerpt,
`RetainedContent` and all summary tiers were cut and have stayed cut.

### Round 2 killed the plan/apply split (v3)

v3 tried to avoid round 1 by having the planner emit an `Elisions` list that
the host applied at request-build time. All three reviewers rejected it:

**Verified: the index space was wrong.** `Elision.Index` indexed
`PlanInput.Messages`, but every consumer holds `Preparation.Messages`, which is
the retained *subset* (`messagesFromIndexes`, `planner.go:206,275`). Every
elision would land on the wrong message - and a stub landing on the objective
would forge a user turn.

**Verified: retention became unbounded.** v3 kept full bodies in `retained`
while admitting units on *stub* cost. `Candidate.ActiveContext =
MarshalCanonical(retained)` (`planner.go:113`) is written verbatim on every
commit (`storage/context_store.go:184`), so the admission budget measured
stubs while the write stored bodies. `limits.go` states the assumption v3
falsified: a checkpoint's "natural ceiling is the model's prompt budget, which
the planner already enforces upstream". Under an operator-set checkpoint bound
this is the INV-AG-35 wedge class - one over-limit commit refuses the turn and
`active_context` only grows.

**Verified: the mitigation v3 leaned on does not exist.**
`promptBudgetErrorWithTools` runs only when `opts.PreparationManager == nil`
(`agent/context.go:14`); on the prepared path nothing re-checks cost before
send, so a host that forgot to apply the plan would ship an over-budget
request with no local error.

**Verified: §3.3 contradicted its own acceptance test.** "Elide all but the
single newest tool result" cannot help the single-oversize-newest-result case,
which is exactly what the test asserted must now pass.

**Verified and embarrassing: `tool_name` is model-authored.** v3 asserted it
was "host-known metadata, nothing to sanitize". It is copied verbatim from the
model's own tool call - `Name: r.toolCall.Function.Name`
(`internal/agent/loop_tools.go:49`) - and nothing bounds it
(`validateToolCall`, `provider/context.go:181`). A rejected call's error body
goes through `failToolTask` without `capToolResult` (`loop_tools.go:374`), so
a model-chosen name is an unbounded, control-character-bearing string that
would have been wrapped in system-authored framing and re-sent every turn.

**Rejected / recorded as disproved:** tool pairing cannot break under stubbing
(selection is unit-granular, `unitSelected`, `planner.go:254`); elision cannot
cause a spurious compaction (`before` is pre-elision, `planner.go:77`); Go map
iteration is not a nondeterminism source (`messagesFromIndexes` sorts,
`planner.go:280`); exact `<N> bytes` in a stub is not a meaningful disclosure,
because the provider already received the full body earlier in the same run
under the same fenced binding.

### The insight that produces v4

Round 2's structural reviewer noticed the durable projection is **turn-scoped**:
`contextTurnMessages` slices from the latest user message
(`internal/chat/context_integration.go:439`) and only that slice reaches
`ProjectSource` (`context_publication.go:36`). Verified. So messages **before
`objectiveIndex`** are not in this turn's projection at all - they were
committed by their own turns. Rewriting *those* bodies in place destroys no
durable copy, which is precisely what round 1's blocker forbade for same-turn
results.

That single age boundary removes the need for everything v3 invented.

## 3. Design (v4): in-place elision of already-committed bodies

### 3.1 The age boundary is the whole safety argument

A `RoleTool` message is an elision candidate iff **all** hold:

- index < `objectiveIndex` (it belongs to a turn that already committed);
- `len(Content) > elideThresholdBytes` (package constant, 2048);
- the stub is strictly cheaper *in tokens* than the body it replaces
  (`estimateTokens`, `provider/context.go:12` - compared in tokens, not bytes);
- it is not in the mandatory set.

This is a **pure predicate evaluated once, before selection** - not an
incremental loop. Every message therefore has a fixed cost before the
selection pass runs, so the algorithm is single-pass, terminating, and
order-independent by construction (round 2 correctness finding 5).

Candidates are rewritten in the planner's *cloned output*, never in
`input.Messages`.

### 3.2 One view, one set of numbers

Because the stub replaces the body in `retained`, everything downstream
describes the same bytes: `PlanResult.Messages`, `Candidate.ActiveContext`,
`AfterTokens`, the idempotency fingerprint, and the outbound request. There is
no `Elision` type, no index space, no shadow cost model, no host duty at N call
sites, and no dual token accounting - the four defects round 2 found in v3
cannot be expressed in this design.

`active_context` gets *smaller*, so the round-2 unbounded-retention blocker is
answered by construction rather than by a new byte cap, and elision genuinely
reduces at-rest exposure instead of inverting the claim.

### 3.3 Admission: a separate, bounded elided tail

`RecentTail` (8) keeps bounding **full-fidelity** messages. Elided units are
admitted under a *second explicit ceiling*, `maxElidedTailMessages` (constant,
64 - the existing `maxRecentTailMessages`), and still only while cost stays
under target. Bounded count times bounded per-message size = bounded
`active_context`. This is the change that makes the tier do anything; it is
also the one that must never be uncapped.

To keep the widened scan cheap, tool-schema cost is hoisted out of the
selection loop (`EstimateRequestCost` re-marshals every `ToolSpec` per call,
`provider/context.go:75`) and selections are costed incrementally.

### 3.4 Stub text

```
[elided tool result: <tool_name>, <N> bytes]
```

`tool_name` is **not** trusted. It is resolved against the tool registry; a
name that does not resolve renders as `unknown`, and any resolved name is
bounded to 64 bytes and restricted to `[A-Za-z0-9_.-]` - replaced wholesale
with `unknown` if it does not match, never stripped in place (stripping
preserves attacker-chosen substrings). No digest, no body excerpt, no
identifier: recall is the follow-on plan's problem and will thread a real
`ContentRef`.

### 3.5 What v4 does NOT fix, stated plainly

- **A single oversize newest tool result still returns
  `ErrPromptBudgetExceeded`.** It is at/after the objective, so it is not a
  candidate. v3's §3.3 tried to fix this and contradicted itself; v4 accepts
  the limit. If it matters, it is a separate change with its own safety
  argument about not yet being committed.
- **The current turn's tool traffic is never elided**, so a single turn that
  outgrows the budget on its own is unaffected.
- **The idempotency key changes** for transcripts that elide - it fingerprints
  the retained set (`planner.go:322,359`), which now contains stubs. That is a
  correct consequence of an algorithm change, not an invariant violation. The
  claim "byte-identical to the pre-change key", asserted in v3 §4/§6, is
  deleted. An in-flight commit across a binary upgrade will miss replay
  detection and surface `ErrStaleRevision` on retry; note it in the release
  notes.
- **Resume rehydrates stubs, not bodies** (`active_context` holds stubs), which
  is consistent rather than surprising - the full body remains in the earlier
  turn's source events and in the checkpoint that committed it.

## 4. Invariants

- `Plan` is a pure function of its inputs; no storage, network or clock, and
  `input.Messages` is never mutated.
- No message at or after `objectiveIndex` is ever elided, so no body is
  rewritten before the turn that produced it has committed.
- `PlanResult.Messages`, `Candidate.ActiveContext`, `AfterTokens` and the
  outbound request all describe the same bytes.
- Retained message count is bounded by `RecentTail` + `maxElidedTailMessages`;
  retained bytes are bounded by the stub size times that count plus the
  full-fidelity tail.
- Elision never increases the request's cost in tokens.
- Tool pairing valid after elision (content-only rewrite; role and
  `ToolCallID` untouched; selection stays unit-granular).
- Stub text contains no model-authored bytes that are not registry-resolved.
- `ErrPromptBudgetExceeded` still raised when elision plus eviction cannot meet
  budget.

## 5. Waves

- **W1** elision predicate + stub renderer (registry-resolved, bounded name),
  with RED tests first: age boundary, threshold edges, token-comparison skip,
  hostile tool names, mandatory-set exclusion.
- **W2** planner integration: predicate before selection, `maxElidedTailMessages`
  admission, incremental costing. **The agent-loop integration test lands in
  this wave** - it is the test that would have caught round 1's blocker.
- **W3** counters (`ElidedMessages`, `ElidedBytes`, `EvictedUnits`) plumbed to
  `Preparation`; `NewCompactionEvent` takes a validated params struct rather
  than growing to nine positional arguments; validator accepts legal zero
  values (`EmitCompaction` swallows constructor errors, `agent/emit.go:83`, so
  a too-strict rule deletes the event instead of reporting it).
- **W4** TUI surface + docs: what compaction keeps, what it stubs, what stays
  recoverable from the committing turn.

Out of scope, tracked separately: the `recall` tool over a real `ContentRef`;
wiring a summarizer at all; the dead
`StructuralPreparationManager.RecentTail`/`OutputReserve` fields; and
`failToolTask` bypassing `capToolResult` (`loop_tools.go:374`), which is an
unbounded-write bug this review found and v4 does not fix.

## 6. Testing

- Age boundary: a transcript whose only oversize tool results are at/after the
  objective elides nothing; move the objective and the same results elide.
- Durability (integration): after a turn whose earlier history was elided, this
  turn's projected source events and the committed `active_context` contain the
  current turn's full bodies, and the previous turn's checkpoint still contains
  the elided bodies.
- Admission: a 60-message transcript retains more than 8 messages when the
  extras are elided, never more than `RecentTail + maxElidedTailMessages`, and
  the retained byte count stays bounded.
- Determinism: same input planned twice yields identical retained sets, stubs
  and keys; the key is a pure function of the retained messages.
- Token invariant: a body whose stub costs more in tokens is not elided.
- Hostile name: a tool call named with 1 KiB of control characters renders as
  `unknown` and the stub stays under its bound.
- `ErrPromptBudgetExceeded` still returned for a single oversize newest result.
- Loop integration: outbound request valid under `ValidateToolPairing`, cost
  equals `AfterTokens`.

## 7. Failure analysis

- **Model treats a stub as real output.** It says "elided" and gives the size;
  worst case it re-runs the tool, which is today's behaviour.
- **A body is elided before its turn commits.** Prevented by the age boundary
  and pinned by the durability integration test; this is the failure that would
  matter most, so it is the one with a dedicated test.
- **Elided tail grows.** Hard-capped by `maxElidedTailMessages`; the cap is a
  constant, not a knob, precisely so it cannot be raised into the wedge.
- **Sparse retention shreds causality.** Admitting old stubbed units while
  skipping others yields a non-contiguous history. Policy: never admit a unit
  older than the newest rejected unit, so the retained set stays a suffix.

## 8. Scorecard (v4, self-assessed - pending round 3)

| Criterion | Status |
|---|---|
| Compiles / no import cycles | PASS |
| No breaking public API (internal packages only) | PASS |
| Planner stays pure and testable in isolation | PASS |
| No new config knob | PASS |
| Durable behaviour: same-turn bodies preserved | PASS |
| Retained set bounded in count and bytes | PASS |
| Round 1 + round 2 findings dispositioned | PASS (§2) |
| Third challenge round on v4 | **NOT RUN - gate open** |

**Rollback criterion:** if the age boundary leaves too little elidable material
to matter - measure it on a real long session before W2 - close the plan and
take the token cost rather than ship machinery that saves nothing.
