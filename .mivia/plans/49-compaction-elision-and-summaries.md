# 49 - Compaction: tool-result elision tier

**Status:** DESIGN v3 - rewritten 2026-08-02 after the ADLC Step 0 challenge
round returned RETURN-TO-DESIGN from all three reviewers (structural,
correctness, security). §2 records the dispositions. Needs a second challenge
round before lock.
**Date:** 2026-08-02
**Depends on:** `41` (structural compaction, shipped), `48` (bounded results).
Coordinates with `42` and `43` without blocking them.
**Blast radius:** HIGH - compaction planner semantics, provider request
construction, message shape invariants, observability.

## 1. Problem

`retainMessages` (`internal/contextmgr/planner.go:159`) keeps {system, latest
objective, latest tool unit} plus whole units from the tail, and drops
everything else with no stub and no pointer. Units are all-or-nothing
(`messageUnits`, `planner.go:215`): one huge `grep` result evicts the sibling
assistant reasoning with it. The model re-derives lost findings with fresh
tool calls; the waste shows up as repeated work, not as a cost line.

## 2. Step 0 challenge dispositions

Three hostile reviews ran against v2 of this plan. Findings that changed the
design, with the verification that confirmed them:

**CONFIRMED - blocker: elision must not happen inside the planner.**
`Plan`'s output is written back over live history - `l.Messages =
clonePreparedMessages(preparation.Messages)` (`internal/agent/context.go:47`),
and end-of-turn projection reads that same array (`ordered :=
contextTurnMessages(loop.Messages, userText)`, `internal/chat/session.go:346`
→ `ProjectSource`, `internal/chat/context_publication.go:36`). A stub written
by the planner would therefore replace the durable copy of same-turn tool
results, which today survive to commit. v2 claimed "the body was being dropped
either way"; for in-turn results that is false. **Resolution: the planner
computes an elision plan; only the provider-request builder applies it.**

**CONFIRMED - blocker: stubbing alone cannot save units that the count cap
evicts.** `retainMessages` admits tail units while `tailCount+len(unit) <=
tailLimit` (`planner.go:189`, default 8). Selection returns only selected
indexes (`planner.go:206`), so a message that is stubbed but not selected is
discarded *with its stub*. v2's headline claim - "elision alone reaches target,
zero units evicted" - only held for transcripts that already fit in 8
messages. **Resolution: `RecentTail` counts full-fidelity messages only;
elided units are admitted on cost alone.**

**CONFIRMED - the recoverability story addressed a store that does not exist
that way.** `newContentRef` hashes the *stored* bytes after
`redactSourcePayload` may have rewritten them (`sanitize.go:61-62,137`), the
row key is `contentRefID(principal, digest)` - principal-scoped, not the bare
digest (`sanitize.go:164`) - and `ReadPayload` requires a complete
`ContentRef` with owner cross-checks (`internal/storage/context_source.go:107`,
`WHERE ref=?`). A 12-hex prefix is not a lookup key, and the digest of the live
body misses every redacted payload. **Resolution: `RetainedContent`, the
`recoverable` marker and the `sha256:` field are cut from this plan. Recall is
the separate plan's problem, and it will thread a real ref.**

**CONFIRMED - the excerpt line was the only part that could leak, and its gate
was backwards.** `contextRedactionPolicy` builds `Redactor` from
`policy.Text` (`internal/cli/context_setup.go:63`), which applies **patterns
only** - `redaction_key_names` are honoured in `matchesKey`/`JSONValue`
(`internal/redact/redact.go:114`), so a key-names-only workspace passes
`policyConfigured` while the redactor is a no-op. The planner has no policy
with which to classify anything, `Summarize` deliberately strips policy from
anything provider-facing (`summarizer.go:47-49`), and a 120-*byte* cut splits
UTF-8, which `SanitizeSourcePayload` rejects outright (`sanitize.go:48`) -
failing a completed turn, the exact regression class the comment at
`sanitize.go:51-60` says was already paid for once. **Resolution: no excerpt,
no digest, in this plan.**

**CONFIRMED - §2.2/§9 of v2 were factually wrong about what is at rest.**
`Candidate.ActiveContext = MarshalCanonical(retained)` (`planner.go:113`) is
raw message content with no policy applied, written to
`context_checkpoints.active_context` on every commit
(`internal/storage/context_store.go:184`). The "unconfigured install stores
nothing" guarantee covers `context_payloads` only. So elision does not add
at-rest exposure - it *reduces* it - and the honest recall source for a
default install is the previous checkpoint, not the payload store. v2's open
question was misframed and is withdrawn.

**CONFIRMED - the mandatory set exempts exactly the message that overflows.**
`markLatestToolUnit` (`planner.go:241`) pins the latest assistant-with-tool-
calls unit whole, and `selectedCost > input.Budget` hard-fails before any tail
work (`planner.go:176`). A single 400 KB result still returns
`ErrPromptBudgetExceeded`. It also picks the latest unit *that has tool calls*
however old, so an ancient huge result can be pinned forever while fresh cheap
ones are elided. **Resolution: last-tier elision of the mandatory unit before
erroring; re-scope the selector to "latest unit, if it is a tool unit".**

**CONFIRMED - smaller items.** A stub can be longer than the body it replaces
(`estimateTokens` is `len/4`, `internal/provider/context.go:12`), and
`CompactionEvent.Validate` rejects `AfterTokens > BeforeTokens`
(`internal/events/event.go:107`) while `EmitCompaction` swallows the
constructor error (`internal/agent/emit.go:83`) - a cost-increasing elision
would silently delete the event. `[context]` is documented as "every field
defaults to 0 = uncapped" (`internal/config/types.go:27`), so a
`0 = disabled` knob inverts that table's contract; and
`StructuralPreparationManager.RecentTail`/`OutputReserve` are already dead
fields nothing reads (`structural.go:18,39-43`). `SummaryStatus` has three
unreachable values while no summarizer is wired (`cli/context_setup.go:104`).

**REJECTED.** A session-local HMAC stub handle, and "put the full 64-hex
digest in the stub": both solve identification, which this plan no longer
attempts. They belong to the recall plan. **DISPROVED by the reviewers, and
worth recording:** tool pairing cannot break under stubbing - selection is
unit-granular (`unitSelected`, `planner.go:254`), so a stub's paired assistant
call can never be dropped while the stub is kept; and elision cannot cause a
spurious compaction, because `before` is computed pre-elision
(`planner.go:77`) and the `before < trigger` early return precedes selection.

**Summaries are removed from this plan entirely.** No summarizer is wired
(`cli/context_setup.go:104` passes none; `commit.go:29` requires non-nil), so
every summary tier was speculative. Wiring one is a separate decision with its
own security surface, tracked as a follow-on.

## 3. Design (v3)

### 3.1 The planner computes an elision plan; it never rewrites a message

```go
type Elision struct {
    Index         int    // index into PlanInput.Messages
    ToolName      string // message.Name, for the stub text
    OriginalBytes int
}

type PlanInput struct { /* existing */ ElideThresholdBytes int }
type PlanResult struct {
    /* existing */
    Elisions       []Elision
    ElidedBytes    int
    EvictedUnits   int
}
```

- `PlanResult.Messages` keeps **full bodies**. `Candidate.ActiveContext`,
  `ProjectSource`, and the idempotency key are all computed from full bodies,
  exactly as today - so durable history, at-rest content and commit
  idempotency are all byte-identical to current behaviour.
- Costing uses a shadow view: a candidate message costs `min(len(body),
  len(stub))`. Selection, the target comparison and `AfterTokens` all use the
  shadow cost, because that is what actually goes on the wire.
- Candidates: `RoleTool` messages whose `len(Content) > ElideThresholdBytes`
  (package constant `defaultElideThresholdBytes = 2048`; no config knob in
  this plan). Applied **incrementally, oldest first, stopping as soon as the
  retained shadow cost is under target** - not "every candidate".
- A candidate is skipped when its stub would not be smaller than its body.

### 3.2 Selection stops counting elided messages against the tail cap

`RecentTail` exists to bound how much *full-fidelity* recent context is kept.
An elided message is not full fidelity, so it must not consume that budget:
`tailCount` increments only for messages retained whole. Cost remains the
binding constraint for elided units. This is the change that makes the tier do
anything at all (§2, second blocker).

### 3.3 Last-tier elision of the mandatory unit

If `selectedCost > Budget` with the mandatory set alone, elide the mandatory
tool unit's bodies too - all but the single newest tool result - before
returning `ErrPromptBudgetExceeded`. A turn that would have hard-failed now
proceeds with stubs. `markLatestToolUnit` is re-scoped to consider only the
final unit, so an ancient tool unit is no longer pinned.

### 3.4 The host applies the plan when building the request

`Preparation` carries `Elisions`. The two request builders - the agent loop
and chat's plain path - substitute the stub text for those message indices in
the outbound `provider.Message` slice only. `l.Messages` / `s.Messages` /
`ordered` are untouched.

Stub text, host-rendered, deterministic, no identifier and no body excerpt:

```
[elided tool result: <tool_name>, <N> bytes]
```

`tool_name` is host-known metadata, not model-authored content, so there is
nothing to sanitize; it is length-bounded and control-characters stripped
regardless.

### 3.5 Observability

`NewCompactionEvent` takes a validated params struct instead of growing to
nine positional arguments, and gains `ElidedMessages`, `ElidedBytes`,
`EvictedUnits`. The validator must accept legal zero values - `EmitCompaction`
discards constructor errors (`emit.go:83`), so a too-strict rule deletes the
event rather than reporting it. A compaction that still evicts units after
full elision says so: that is the case where work is genuinely lost.

## 4. Invariants

- `Plan` remains a pure function of its inputs; no storage, network or clock.
- `PlanResult.Messages`, `Candidate.ActiveContext` and the idempotency key are
  computed from full bodies - identical to today, so nothing storage-derived
  can perturb the commit `OperationID`.
- Durable projection persists full tool bodies; elision is request-scope only.
- Tool pairing valid in the emitted request (stub substitution preserves role
  and `ToolCallID`; selection stays unit-granular).
- The newest tool result is never elided except by §3.3, and never when it is
  the only way to stay under budget without erroring.
- Elision never increases the emitted request's cost.
- `ErrPromptBudgetExceeded` still raised when elision plus eviction cannot
  meet budget.

## 5. Implementation steps / waves

- **W1** RED tests then implementation for the shadow cost model and the
  `RecentTail` change: candidate selection, incremental oldest-first stop,
  stub-not-smaller skip, threshold edges, mandatory set untouched.
- **W2** `PlanResult.Elisions` + counters; **agent-loop integration test lands
  in this wave**, not later - it is the test that would have caught the
  durable-history blocker.
- **W3** Host application at request construction (agent loop + chat plain
  path); integration test asserting `l.Messages` and the projected source
  events still carry full bodies while the outbound request carries stubs.
- **W4** `CompactionEvent` params struct + counters + TUI surface.
- **W5** Docs: what compaction keeps, what it stubs on the wire, and what
  remains recoverable from the previous checkpoint.

Separate follow-ons, explicitly out of scope: the `recall` tool over a real
`ContentRef`; wiring a summarizer at all; deleting the dead
`StructuralPreparationManager.RecentTail`/`OutputReserve` fields.

## 6. Testing

- Planner: transcript with 3 large tool results at 85% budget - elision alone
  reaches target with zero units evicted, and the same transcript on today's
  code evicts units. Assert the count, not the direction.
- `RecentTail` semantics: a 60-message transcript retains more than 8 messages
  when the extra ones are elided, and still at most 8 full-fidelity ones.
- Shadow cost: `AfterTokens` equals the cost of the stubbed request, and
  `after > Budget` is evaluated on that same view.
- Stub-not-smaller: a body just over threshold is not elided.
- §3.3: a single oversize newest tool result proceeds instead of returning
  `ErrPromptBudgetExceeded`; when even that is not enough, the error still
  comes back.
- Idempotency: key is byte-identical to the pre-change key for a transcript
  that triggers elision (proves stubs never reach the fingerprint).
- Durability (integration): after a turn whose history was elided, the
  projected source events and `active_context` contain the full bodies.
- Loop integration: the outbound provider request carries stubs, is valid
  under `ValidateToolPairing`, and is under budget.

## 7. Failure analysis

- **Model treats a stub as real output.** It says "elided" and gives the size;
  worst case it re-runs the tool, which is today's behaviour.
- **Re-planning churn.** Full bodies stay in history, so every plan re-derives
  the same elision set from the same inputs - deterministic, no oscillation,
  and no growth in stored state.
- **Host forgets to apply the plan.** The request is then merely larger than
  planned, and the existing budget check at send time catches it; a test
  asserts request cost matches `AfterTokens`.
- **A future caller persists the stubbed request.** Guarded by the durability
  integration test above, which asserts on projected events rather than on the
  request.

## 8. Scorecard (v3, self-assessed - pending re-challenge)

| Criterion | Status |
|---|---|
| Compiles / no import cycles (planner gains no dependency) | PASS |
| No breaking public API (all internal packages) | PASS |
| Testable in isolation (planner stays pure) | PASS |
| Backward-compatible config (no new knob) | PASS |
| Durable behaviour unchanged | PASS |
| Every new function has a preceding test task | PASS |
| Challenge findings dispositioned | PASS (§2) |
| Second challenge round on v3 | **NOT RUN - gate open** |

**Rollback criterion:** if the `RecentTail` change cannot admit elided units
without breaking unit granularity or pairing, the tier is not worth its
complexity - close the plan and take the token cost.
