# 49 - Compaction: tool-result elision tier

**Status:** SHIPPED / ARCHIVED — implemented on master (`8ff67ae` feat,
`a9cc173` integration tests). Step 0 was re-audited against `c12bf7f`
(2026-08-02). The former reader and marker-forgery blockers remain
deliberately out of scope; §4 records the decisions. Dual-surface operator
transcript vs model context is follow-up plan `52`.
**Depends on:** `41` (structural compaction, shipped). Coordinates with `48`
(uncapped tool-result reliability) but does not wait for it.
**Blast radius:** HIGH — planner semantics, durable active checkpoints,
provider message shape, and content-free compaction telemetry.

## 1. Goal

When structural compaction runs in a multi-turn session, replace an old,
oversized tool-result body with a small, host-authored notice while retaining
the paired tool call and existing message-unit selection. This reduces prompt
cost without modifying live input or relaxing tool-pairing rules.

The tier is intentionally narrow: it does not summarize, recall, expose
historical checkpoints, elide same-turn output, or widen the existing
`RecentTail` admission cap.

## 2. Verified baseline

- `contextmgr.Plan` validates input, calculates `BeforeTokens`, then returns
  the original messages below the threshold. On compaction it calls
  `retainMessages`; the current tail cap is eight optional messages
  (`internal/contextmgr/planner.go`).
- `agent.Loop.prepareStep` installs the preparation result as its live
  history. Replanning after a cancellation therefore makes mutation of
  `PlanInput.Messages` unsafe (`internal/agent/context.go`).
- A completed turn commits the active context into an immutable checkpoint;
  the next turn's planner sees the prior committed active context
  (`internal/contextmgr/commit_request.go`, `internal/storage/context_store.go`).
  Source payloads are metadata-only without a configured redaction policy, so
  they are not a recovery source.
- `Session.Compact` takes the same planner path but supplies no tool schemas,
  a pre-existing accounting mismatch with an agent turn
  (`internal/chat/context_integration.go`). This plan neither depends on nor
  conceals that defect.
- `CompactionEvent` is sealed and content-free; `EmitCompaction` currently
  emits only when `Preparation.Compacted` is true
  (`internal/events/event.go`, `internal/agent/emit.go`).

## 3. Design

### 3.1 Eligibility and replacement

Only after the existing threshold says compaction is necessary, `Plan` clones
the messages and locates the latest user objective. It derives the mandatory
set exactly as `retainMessages` does, then replaces a message only when all of
the following hold:

- it is `provider.RoleTool`;
- its index is before `objectiveIndex`, so it belongs to an earlier completed
  turn;
- it is not mandatory (system, objective, or latest complete tool unit);
- `len(Content) > 2048`; and
- the rendered replacement has a lower `provider.MessageTokens` cost.

The replacement preserves `Role`, `ToolCallID`, `Name`, and `ToolCalls`; only
`Content` changes. It is a constant-format, non-imperative notice with a
rounded byte bucket, for example:

```
[context elided prior tool result; original size about 4 KiB]
```

Buckets are powers of 1024 rendered as KiB/MiB. The renderer must be pure and
must never return a string whose estimated content cost is greater than or
equal to the original. No digest, excerpt, identifier, tool name, or tool
arguments is added.

`Plan` passes this one cloned array to all selection, costing, fingerprint,
canonical-marshalling, and returned-message paths. If the request is below the
existing compaction trigger, it returns an unchanged clone and reports zero
elision. `BeforeTokens` always describes the original request; `AfterTokens`
describes the returned messages.

### 3.2 Retention and bounds

The existing message-unit algorithm stays intact. In particular:

- `defaultRecentTailMessages` remains 8 and the validation ceiling remains
  64; there is no `maxElidedUnitMessages`, skip-on-over-target change, or
  widened checkpoint admission in this plan.
- mandatory anchors and the latest complete tool unit are still retained
  whole. They may exceed the target and, as today, only fail when they exceed
  the hard prompt budget.
- optional units still stop at the first unit that would exceed `target`.

This deliberately avoids increasing checkpoint size or changing the
retention shape for transcripts with no eligible result. It also means this is
an optimization, not a cure for an enormous latest or same-turn result.

### 3.3 Accounting and data model

Keep cost calculation simple and correct: call the existing estimator over the
same candidate arrays as today. Hoisting tool-schema marshaling or introducing
incremental accounting is not justified by an eight-message optional scan and
is out of scope.

`PlanResult` gains content-free counters:

```go
ElidedMessages int
ElidedBytes    int
```

They are zero for an unchanged/below-trigger plan. `Preparation` carries the
counters. `Loop` adds private per-turn `elidedMessages`, `elidedBytes`, and
`compacted` accumulators, resets them at the start of `Run`, and copies their
totals into every successful `LastPreparation`. This matters because a step can
elide enough content to fit before the final step that commits. A failed turn
still discards its preparation and emits nothing.

Extend the sealed `events.CompactionEvent` with non-negative
`ElidedMessages` and `ElidedBytes`, use a params struct constructor, and emit
when the turn's accumulated `Compacted` flag is true. The event uses the first
compacting preparation's `BeforeTokens` and the last compacting preparation's
`AfterTokens`; later non-compacting steps can add messages, so using the final
step's cost would violate the event's `AfterTokens <= BeforeTokens` contract.
The event remains content-free. Existing consumers that do not display the
counters remain valid; the TUI may add a short count to its current compaction
line but does not get a new retrieval surface.

The new internal seams are deliberately small:

```go
type ElisionStats struct { Messages, Bytes int }

func mandatoryIndexes(messages []provider.Message, objectiveIndex int) map[int]struct{}
func elideToolResults(messages []provider.Message, objectiveIndex int) ([]provider.Message, ElisionStats)

type CompactionEventParams struct {
    Trigger string
    BeforeTokens, AfterTokens int
    ElidedMessages, ElidedBytes int
    SourceRange contextstate.SourceRange
    SummaryVersion uint32
}
func NewCompactionEvent(CompactionEventParams) (CompactionEvent, error)
```

`mandatoryIndexes` replaces the local mandatory-set construction in
`retainMessages`, preventing eligibility and selection from drifting. All
helpers stay unexported except the event constructor and its parameter type;
no provider wire type changes.

## 4. Closed Step 0 decisions

### 4.1 A marker is advisory, not an authority boundary

The notice is text sent to the model. A tool body can imitate it, just as it
can imitate any other model-facing text. No host code parses it, grants
authority because of it, or uses it to decide retention, so forgery cannot
change program behaviour or access control. Do not add an `Elided` field just
to serialize the same string, and do not rewrite every tool result to escape a
label: that would mutate same-turn, uncommitted output for no security gain.

The wording remains clearly host-authored, descriptive, and non-imperative;
the paired assistant tool call already identifies the tool and arguments.

### 4.2 No historical-checkpoint reader

Each eligible body existed in the active checkpoint committed before the turn
that elides it. That is sufficient rollback evidence; no code currently offers
historical checkpoint reads. Adding `mivia context show` would expose raw
active-context bytes under a new authorization/redaction contract and is a
separate product and privacy decision. This plan must not create that command,
store interface, or a model `recall` tool.

Thus elision is irreversible from the user-facing product today. It is only
applied to already committed history, and ordinary session deletion continues
to govern the retained checkpoints. If a future reader is approved, it must be
designed against tombstones, principal capability checks, and read-time
redaction independently of this plan.

### 4.3 Measurement is rollout evidence, not a build blocker

There is no representative session corpus in this repository, so a claimed
pre-implementation byte measurement would be invented. The counters above
measure eligible bytes at the precise preparation step where compaction runs.
The first release decision is based on those aggregate, content-free counters;
do not collect tool text or add telemetry that exports it. If observed savings
are immaterial, remove the tier in a follow-up rather than widening admission.

## 5. Invariants

- `Plan` remains pure and does not mutate `PlanInput.Messages`.
- No message at or after `objectiveIndex` is elided.
- Selection, costing, key derivation, checkpoint bytes, and returned messages
  use one cloned, elided array.
- No role, `ToolCallID`, name, or tool-call list changes; retained histories
  pass `provider.ValidateToolPairing`.
- Elision strictly lowers the replaced message's estimate and never raises the
  returned request cost relative to the original retained selection.
- The existing tail and hard-budget behavior is unchanged apart from eligible
  body content; mandatory overflow still returns `ErrPromptBudgetExceeded`.
- Events and logs expose aggregate counts only, never content, names, hashes,
  or individual original lengths.

## 6. Delivery slices

| Wave | Files | Work and RED-first gates |
|---|---|---|
| 1 | `internal/contextmgr/planner_test.go`, `planner.go` | Add failing eligibility, bucket, immutability, cost/threading, deterministic-key, and tool-pairing tests; implement the pure clone-and-elide helper and thread it through `Plan`. Run `go test -run 'TestPlan.*Elid' ./internal/contextmgr`. |
| 2 | `internal/contextmgr/contracts.go`, `internal/agent/context.go`, their tests | Carry the two counters through preparation; add private per-turn loop accumulation, including first-before/last-compacting-after event accounting, without changing message content. Run focused `contextmgr` and `agent` tests. |
| 3 | `internal/events/event.go`, `internal/events/serialize.go`, `internal/agent/emit.go`, `internal/chat/context_publication.go`, their tests | Add validated content-free event counters and emit one event for an accumulated compaction. Test direct loop and chat publication paths. |
| 4 | `internal/agent/context_loop_test.go`, `internal/chat/context_session_test.go`, `internal/cli/tui_events.go`, relevant tests | End-to-end durable/history and visible-event coverage; display counts only when nonzero. Update the owned compaction documentation if one exists; otherwise do not add a new product document. |

Each production edit follows a compiling assertion-failing test in its paired
test file. Wave gates are `go test -race` for the affected packages; the final
gate is the repository verification command in §8.

## 7. Required tests

- An oversize tool result at or after the latest user objective is unchanged;
  moving the objective after it makes it eligible.
- Threshold edge: below trigger returns byte-equivalent cloned messages and
  zero counters; exact trigger can elide.
- Mandatory system/objective/latest-tool-unit messages never elide.
- Threshold edges at 2048/2049 bytes, bucket boundaries, empty content, and a
  rendered notice that is not cheaper all preserve original content.
- Input immutability, deterministic repeated plans, and idempotency-key change
  only when the returned retained bytes change.
- `AfterTokens` equals a fresh estimate of `PlanResult.Messages`; pairing stays
  valid and the hard mandatory-overflow error remains.
- A completed multi-turn session commits the elided active context while the
  immediately preceding checkpoint still contains the original body; no test
  reads that prior checkpoint through a new product API.
- A step that elides and fits still contributes counters to the final sealed
  event; no serialized event contains content, tool name, hash, or an
  individual original size.
- Existing no-elision histories retain their current eight-message behavior.

## 8. Verification and rollback

Before commit, run:

```text
go test -race ./internal/contextmgr ./internal/agent ./internal/chat ./internal/events ./internal/cli
make validate-invariants
make invariants
make verify
```

Run `make build` and exercise `/compact` plus a multi-turn tool session
manually if the test configuration can do so without live provider calls.

Rollback the feature rather than widening retention if it changes a
non-eligible transcript, breaks pairing, changes a no-elision checkpoint, or
the content-free counters demonstrate immaterial savings in real use.

## 9. Out of scope

- Summarizer wiring or model-generated summaries.
- Eliding current-turn results or subagent single-task histories.
- Historical checkpoint, source-payload, or model recall readers.
- New configuration knobs, admission-cap widening, schema migration, or
incremental cost-estimator optimization.
- The pre-existing `Session.Compact` omission of tool schemas.
