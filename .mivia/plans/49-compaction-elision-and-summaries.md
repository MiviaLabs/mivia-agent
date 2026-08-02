# 49 - Compaction: tool-result elision tier

**Status:** BLOCKED - two unresolved design problems (§11) and an unrun
measurement (§2.3) stand between this and implementable. Do not start W2.
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
evicts the sibling assistant reasoning with it. The model re-derives lost
findings with fresh tool calls.

The fix is a middle tier between "keep the whole unit" and "drop it": keep the
tool call and the reasoning around it, replace only the oversized body with a
stub.

## 2. What elision may touch, and why

### 2.1 Only bodies from turns that already committed

Compaction runs inside the tool loop and its output becomes the live history:
`l.Messages = clonePreparedMessages(preparation.Messages)`
(`internal/agent/context.go:47`). End-of-turn publication reads that same array
- `ordered := contextTurnMessages(loop.Messages, userText)`
(`internal/chat/session.go:346`) - and `contextTurnMessages` slices from the
latest user message (`internal/chat/context_integration.go:439`), so the
projection is turn-scoped.

A tool body at or after `objectiveIndex` therefore has not been persisted
anywhere yet: rewriting it destroys the only copy. A body before
`objectiveIndex` was written by the turn that produced it.

**This boundary is absolute.** No path elides a same-turn body. An argument of
the form "this turn was going to fail anyway, so nothing would have been
committed" does not hold here: `finishAgentTurn` skips the commit only when the
turn error is *not* an interruption (`internal/chat/session.go:313-324`), and
`Session.Compact` commits unconditionally and then overwrites live history
(`internal/chat/context_control.go:100-121`). Eliding uncommitted bodies would
need an explicit caller-supplied capability, not an inference about another
package's error handling.

### 2.2 Where the surviving copy actually is

"Committed by its own turn" is weaker than it sounds, and this design depends
on being precise about it:

- **Source events usually hold no bytes.** `ProjectSource` calls
  `SanitizeSourcePayload` (`internal/contextmgr/source_projector.go:47`), which
  returns `HashOnly` - ref, size and digest, no data - whenever the workspace
  has no redaction policy (`internal/contextstate/sanitize.go:61-70`), and
  `contextRedactionPolicy` yields the zero policy unless `[privacy]` patterns
  or key names are set (`internal/cli/context_setup.go:55`), which have no
  defaults.
- **The checkpoint is the real copy.** `BuildCommitRequest` marshals
  `result.Active` (`internal/contextmgr/commit_request.go:46`) into
  `CheckpointRecord.ActiveContext`, written verbatim
  (`internal/storage/context_store.go:184`). Checkpoint rows are never deleted.
- **Nothing reads a historical checkpoint.** `Load` resolves only
  `active_checkpoint_id`, and resume decodes only that
  (`internal/chat/context_integration.go:160-175`).

So today an elided body would be *on disk and unreachable from every surface*.
A reader is therefore a precondition of elision - and building one is itself an
open problem (§11.2), because the only existing cross-session read path is
gated on `(workspace_id, subject_id)` with no capability
(`internal/storage/chat_sessions.go:49`), `subject_id` is hardcoded
`"local-user"` (`cli/context_setup.go:98`), and `active_context` never passes
the redaction boundary that `SanitizeSourcePayload` imposes on payloads. A
reader in that shape would turn unreachable at-rest unredacted tool output into
a browsable archive - a privacy regression traded for recoverability.

### 2.3 Where it does nothing

The tier is worthless for subagents. `multi_step` seeds a loop with one task
prompt (`internal/subagents/multi_step.go`), so every message is at or after
the objective and nothing is ever a candidate - in exactly the loops that
generate the largest tool results. It is equally worthless within a single long
turn, for the same reason. Its value is confined to long multi-turn sessions.

**Measure before building.** Compaction most often fires *mid-turn* in agentic
loops, where the bloat is same-turn and therefore ineligible. So the
measurement must be elidable bytes **at the moment the trigger fires**, not
across whole sessions - a whole-session figure would count bytes this tier
cannot reach. If that number is small, close the plan: much of the remaining
benefit is attributable to the wider admission ceiling (§3.2) rather than to
elision, and a ceiling change is a far smaller proposal.

## 3. Design

### 3.1 Elision is a predicate over a cloned array

`Plan` builds `elided := applyElision(cloneMessages(input.Messages))` **before**
calling `retainMessages`, and threads that array through selection, costing and
the retained result. `input.Messages` is never mutated - `Plan` returns errors
after selection (`planner.go:104,107`) and the loop re-plans the same input on
the cancellation path (`agent/context.go:31`), so an in-place edit would
corrupt live history on a failed plan.

Every costing site must read the elided array, not `input.Messages`
(`costForSelected` at `planner.go:172,196`; `messagesFromIndexes` at
`planner.go:206`). Missing one yields the inverse bug: selection costed on
stubs, retained set carrying full bodies.

A message is elided iff **all** hold:

- `Role == RoleTool`;
- index < `objectiveIndex`;
- not in the mandatory set (system, objective, latest tool unit);
- `len(Content) > elideThresholdBytes` (package constant, 2048);
- the stub is strictly cheaper in tokens (`estimateTokens`,
  `provider/context.go:12` - the unit `EstimateRequestCost` sums).

The predicate is evaluated once, before selection, so every message has a fixed
cost when selection runs: single-pass, terminating, order-independent. An
"elide until under target" loop would make elision depend on selection and
selection on elision, with no fixed point.

### 3.2 Admission, and what is actually bounded

Full-fidelity messages keep the existing `RecentTail` cap (8,
`planner.go:17`). A unit containing **at least one stub** counts instead
against `maxElidedUnitMessages` (a new constant, 64 - deliberately not a reuse
of `maxRecentTailMessages`, which is the validation ceiling for `RecentTail`
and must not acquire a second meaning). Stated as "at least one stub", not "all
oversize results are stubs": the latter is vacuously true of a unit with no
oversize results, which would admit 60 messages of plain prose where today 8
are kept.

The widened ceiling is also not reachable through the existing scan as it
stands: `planner.go:200` **breaks** on the first over-target unit rather than
skipping it, so admission stops at the first expensive unit regardless of how
cheap older stubbed ones are. Turning that into a skip delivers the ceiling but
changes retention for transcripts that elide nothing, which must be an explicit
decision with its own test - not a side effect.

Retention is **not** a contiguous suffix, and this design does not claim it:
index 0 and `objectiveIndex` are mandatory anchors with a hole between them,
and the existing loop already skips a unit that does not fit while continuing
to scan (`continue`, not `break`, at `planner.go:189`). Converting that to a
strict suffix would reduce today's retention.

The honest bound on retained bytes is the target check itself
(`planner.go:200`): retained cost stays under `target` = Budget/2. Two
exceptions are real and are stated rather than papered over:

- the **mandatory set is unbounded** - `markLatestToolUnit` (`planner.go:241`)
  pins an entire unit, so one 50 MB tool result in it is retained verbatim;
- `active_context` does **not** necessarily shrink. Admitting up to 64 elided
  messages that are dropped today can make a checkpoint larger than the current
  8-message one, bounded by `target` = Budget/2 - roughly `2 x Budget` bytes
  per commit, on rows that are never deleted. Under an operator-set
  `max_checkpoint_bytes` (`internal/config/types.go:41`) that growth is what
  **refuses the turn**: INV-AG-35 states that publication is one transaction,
  so exceeding a volume bound rejects everything and, because an active context
  only grows, one refusal wedges the session permanently. Admission must
  therefore be bounded by the durable ceiling as well as by `target`.

`EstimateRequestCost` re-marshals every `ToolSpec` per call
(`provider/context.go:75`) and the widened scan calls it more often, so
tool-schema cost is hoisted out of the loop and selections are costed
incrementally.

### 3.3 The stub

```
[elided tool result: ~<size>]
```

**No tool name.** The paired assistant `tool_call` is retained verbatim in the
same unit, carrying the tool's name *and* arguments, so the model already knows
what produced the body. Repeating it would mean either dragging a registry into
`contextmgr` - which has none, only `[]provider.ToolSpec`, an untyped
`map[string]any` (`provider/provider.go:49`) that `Session.Compact` does not
populate at all (`chat/context_integration.go:461`) - or echoing a
model-authored string (`Name: r.toolCall.Function.Name`,
`agent/loop_tools.go:49`) that nothing bounds.

**Bucketed size, not exact.** `SwitchBinding` advances the model without
clearing the active context (`internal/chat/binding.go:187` →
`advanceActiveCheckpoint`, `storage/context_store.go:292`), so stubs reach a
provider that never saw the body. An exact length beside the unelided tool name
and arguments is a size-for-known-argument oracle; a rounded bucket (`~4 KiB`,
`~1 MiB`) keeps the only signal the model needs - whether re-running is worth
it - without one.

**The marker is forgeable, and this is an open problem (§11.1).** Nothing
stops a real tool result from containing `[elided tool result: ...]` verbatim,
letting attacker-controlled text speak in the host's voice. The hook-framing
precedent (INV-AG-34, `internal/agent/hook_context.go:50,99` - a bounded
case-insensitive regex replaced by a provably shorter token) is the right
shape, but neutralising inside the planner does not work: it would rewrite
same-turn bodies whose only copy is uncommitted, and it never runs at all on a
sub-trigger turn (`planner.go:91`), which is exactly when a forged stub would
sit beside genuine ones from an earlier compaction.

**No imperative wording.** The stub states what happened; it never tells the
model to re-run anything. `run_command`, `write_file` and `spawn_agent` results
are elidable like any other, and host-authored text the model has every reason
to obey must not invite duplicated side effects.

No digest, no excerpt, no identifier:

- A digest would be the first path sending a hash of tool output to a
  third-party provider, and a truncated one confirms a guessed body.
- An excerpt is the only part that can carry a secret, and the planner has no
  redaction policy with which to classify one - `PlanInput` carries none, and
  `Summarize` strips policy from anything provider-facing
  (`summarizer.go:47`). A byte-bounded excerpt would also split UTF-8, which
  `SanitizeSourcePayload` rejects (`sanitize.go:48`), failing a completed turn.
- An identifier would not resolve: payload rows are keyed by
  `contentRefID(principal, digest)` (`sanitize.go:164`), the digest is over
  post-redaction bytes (`sanitize.go:61,137`), and `ReadPayload` needs a
  complete owner-scoped `ContentRef` (`storage/context_source.go:107`).
  Recovery goes through the checkpoint reader (§2.2, W1) instead.

## 4. Scope limits

- **A single oversize tool result in the current turn still returns
  `ErrPromptBudgetExceeded`.** It is at or after the objective, so it is never
  a candidate, and §2.1 says why no exception is available.
- **No benefit for subagents, or within a single long turn** (§2.3).
- **The idempotency key changes** for transcripts that elide: it fingerprints
  the retained set (`planner.go:322,359`), which now contains stubs, and that
  set also feeds `NewCheckpointID` (`commit_request.go:71`), so checkpoint
  identity changes too - not merely replay detection. An in-flight commit
  across a binary upgrade surfaces `ErrStaleRevision` on retry. Release-note it.
- **Resume rehydrates stubs**, because the active checkpoint holds them. The
  body remains in the checkpoint of the turn that committed it, reachable via
  the W1 reader.
- **`Session.Compact` plans on a different cost basis** than a turn:
  `prepareInputForContext` sets no `Tools` (`chat/context_integration.go:461`)
  while the turn path does (`agent/context.go:22`). Pre-existing rather than
  caused here, but it means Compact can produce a checkpoint the next turn
  considers over budget. Worth its own fix.

## 5. Invariants

- `Plan` is a pure function of its inputs: no storage, network or clock, and
  `input.Messages` is never mutated.
- No message at or after `objectiveIndex` is ever elided, on any path.
- Selection, costing and the retained result all read the same elided array.
- Retained cost stays under `target`; the mandatory set is the stated exception.
- Elision never increases the request's cost in tokens.
- Tool pairing stays valid: content-only rewrite, role and `ToolCallID`
  untouched, selection unit-granular (`unitSelected`, `planner.go:254`), so a
  stub's paired assistant call can never be dropped while the stub is kept.
- No stub carries model-authored bytes. Forgery by a tool body is NOT yet
  prevented - see §11.1.
- Every body that survived to the end of the turn that produced it is readable
  from that turn's checkpoint. A body produced and evicted *within* one long
  turn is in no checkpoint at all and is not recoverable by any means.

## 6. Waves

- **W0** measurement: elidable bytes across real multi-turn sessions. A
  material figure is the precondition for W2 (§2.3).
- **W1** the reader (§11.2 must be resolved first): `mivia context show`
  over historical checkpoints, own-session only, with a test proving
  a body evicted or elided from the active context is still readable from the
  checkpoint that committed it. This lands *first*, so elision never means
  unreachable.
- **W2** elision predicate + stub renderer: bucketed size, marker
  neutralisation in retained bodies, non-imperative text. RED tests first -
  age boundary, threshold edges, forged-marker input, mandatory-set exclusion.
- **W3** planner integration: cloned elided array threaded through every
  costing site, `maxElidedUnitMessages` admission, incremental costing. **The
  agent-loop integration test lands in this wave** - the durable behaviour is
  the risk, so it is tested beside the change that could break it.
- **W4** observability. Counters (`ElidedMessages`, `ElidedBytes`) accumulate
  on the `Loop` across steps and are emitted independently of `Compacted`:
  `EmitCompaction` returns early when a step did not compact
  (`agent/emit.go:81`) and chat emits only the final step's preparation
  (`chat/session.go:377`), so a step that elided and then fitted is invisible
  today. `NewCompactionEvent` takes a validated params struct rather than nine
  positional arguments, and that struct must not default `SummaryVersion` to 0,
  which `Validate` rejects (`events/event.go:113`) while `EmitCompaction`
  swallows the error.
- **W5** TUI surface + docs: what compaction keeps, what it stubs, and how to
  read an elided body back.

## 7. Testing

- Age boundary: a transcript whose only oversize results sit at or after the
  objective elides nothing; move the objective and the same results elide.
- Durability: after a turn whose earlier history was elided, this turn's
  projected source events and the committed checkpoint carry full bodies, and
  the earlier body is readable through the W1 reader.
- Threading: `AfterTokens` equals the cost of the returned messages - a
  disagreement is the selection-costed-on-stubs inverse bug.
- Admission: a 60-message transcript retains more than 8 messages when the
  extras are elided, never more than `RecentTail + maxElidedUnitMessages`, and
  retained cost stays under `target`.
- Forged marker: a tool body containing the marker verbatim is neutralised in
  the retained set, and neutralising never lengthens the body.
- Determinism: the same input planned twice yields identical retained sets,
  stubs and keys.
- `ErrPromptBudgetExceeded` still returned when the mandatory set alone exceeds
  budget.
- Observability: a step that elides and then fits still reports its counters.
- Loop integration: outbound request valid under `ValidateToolPairing`, cost
  equal to `AfterTokens`.

## 8. Failure analysis

- **Model treats a stub as real output.** It says elided and gives a size
  bucket; worst case it re-runs the tool, which is today's behaviour.
- **A body is elided before its turn commits.** The failure that would matter
  most, prevented by the absolute age boundary and pinned by the durability
  test.
- **An elided body is wanted and cannot be read.** Prevented by ordering W1
  before W2; the durability test is the gate.
- **Elision saves too little to matter.** W0 answers this before the work
  starts. If the figure is small, close the plan and take the token cost - that
  is the rollback criterion.

## 9. Scorecard (self-assessed)

| Criterion | Status |
|---|---|
| Compiles / no import cycles | PASS |
| No breaking public API (internal packages only) | PASS |
| Planner stays pure and testable in isolation | PASS |
| No new config knob | PASS |
| Uncommitted bodies never rewritten | PASS |
| Elided bodies remain readable | PASS (via W1) |
| Retained cost bounded, exceptions stated | PASS |
| Forged-marker prevention | **FAIL - §11.1** |
| Elided body readable | **FAIL - §11.2** |
| Measurement at trigger time | **NOT RUN - §2.3** |

## 10. Out of scope

- **A `recall` tool** exposing elided bodies to the model over a real
  `ContentRef`, with pagination and a per-turn budget. The stub format needs no
  change for it.
- **Summarization.** No summarizer is wired: `cli/context_setup.go:104` builds
  `PreparationCommitter{Store: store}` with a nil `Summarizer`, and
  `CommitPreparation` summarizes only when it is non-nil (`commit.go:29`), so
  the policy gate in `Summarizer.available` (`summarizer.go:75`) is
  unreachable. Wiring one means provider calls on a schedule the user did not
  ask for - a separate decision with its own security surface.
- **Eliding uncommitted bodies.** Would need an explicit caller-supplied
  capability plus a change to `finishAgentTurn` so an interrupted turn carrying
  a budget error does not commit (§2.1).
- **Bugs this design work surfaced, none fixed here:** `failToolTask` writes a
  tool error into history without `capToolResult` (`loop_tools.go:374`), an
  unbounded write under a model-chosen name;
  `StructuralPreparationManager.RecentTail`/`OutputReserve` are dead fields
  (`structural.go:18,39`); and `prepareInputForContext` omits `Tools` (§4).

## 11. Open problems

Both must be solved before this is implementable. Neither is an implementation
detail.

### 11.1 A textual marker cannot be both unforgeable and non-destructive

The stub is a string inside message content, so a tool result can contain one.
Neutralising it has no safe seam:

- in the planner, over all retained bodies - rewrites same-turn bodies whose
  only copy is uncommitted, which §2.1 forbids;
- in the planner, over elidable bodies only - leaves this turn's bodies, the
  ones an attacker just influenced, un-neutralised beside real stubs;
- anywhere in the planner at all - never runs on a sub-trigger turn
  (`planner.go:91`), and stubs from an earlier compaction persist into every
  later turn, so that is the common case, not the edge.

The candidate resolution is to stop making the marker textual: carry
"this result was elided" as a field on `provider.Message` (or neutralise at the
single site where a tool message enters history, `agent/loop_tools.go:365-374`,
including `failToolTask`'s synthesized body, sharing one exported function with
the planner the way `NeutralizeHookTags` is shared today). Either way the rule
must be a bounded case-insensitive regex with the shortest-match arithmetic
written down, as `hook_context.go:28-34` does - a literal-string replace misses
`[ELIDED TOOL RESULT: ...]` and `[ elided  tool result :...]`, which a model
reads identically.

### 11.2 The reader has no authorization model, no addressing, and no surface

`mivia context show` is a precondition (§2.2) with nothing to build on:

- no `context` command family exists in the CLI;
- `checkpoint_id` is a digest of a canonical struct - not something a human
  types - and `turn_id` is an in-process counter that restarts on resume
  (`chat/context_integration.go:282`), with no uniqueness in the schema;
- the store exposes no historical-checkpoint read; the only reader is
  unexported and takes a raw SQL predicate
  (`storage/context_store_recovery.go:78`), consults neither `capability_digest`
  nor `tombstoned`, so a reader built on it would resurrect deleted sessions;
- and it would expose unredacted at-rest content under a weaker gate than
  `Load` (§2.2).

Minimum viable shape: own-session only, resolved through the live principal,
failing closed on a tombstoned session, with redaction applied on the read
path. Cross-session recovery is a separate plan with its own authorization
design.
