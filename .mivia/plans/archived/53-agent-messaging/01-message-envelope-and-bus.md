# 53.01 - Message envelope, event kinds, budgets, config

**Status:** DESIGN - ADLC Step 0 not run.
**Date:** 2026-08-02
**Part of:** program `53` (`00-overview.md`).
**Depends on:** nothing.
**Blast radius:** MEDIUM - new types and config, no behavior change; every
later member builds on these shapes.

## 1. Goal

Define the single message envelope, the fixed message-kind vocabulary, the
event-bus plumbing, and the config surface - without changing any runtime
behavior. After this phase, messages can be constructed, validated,
stamped, published on the internal bus, and persisted to the ledger; nothing
consumes them yet.

Getting the envelope right first is the point: `02`-`04` must not need
envelope changes, and a future external A2A bridge must be a pure mapping.

## 2. Verified baseline

- `internal/events/bus.go`: async pub/sub, per-subscriber bounded queues
  (256, drop-oldest), `Publish` never blocks, `Flush()` barrier. Only the
  TUI subscribes today.
- Events are origin-stamped: `agent.EventOrigin{TaskID, Agent, Depth}`
  (`internal/agent/event.go:47`), applied by `subagents.StampEventOrigin`
  (`internal/subagents/origin.go:11`) and identity-stamped in
  `agentTaskHandler` (`internal/cli/agent_task_handler.go:180`).
- `coordinator.SubscribeLifecycle` (`internal/coordinator/types.go:144`)
  emits `ledger.LifecycleEvent{ID, RunID, Sequence, Kind, TaskID,
  AttemptID, Payload, CreatedAt}` (`internal/ledger/types.go:207`) with
  panic isolation per subscriber - a durable, run-scoped event stream with
  almost no consumers. `Payload` already exists but is documented as
  "bounded, redacted; nil for most events" - message announcements must
  respect that contract (message ID + synopsis only, never bodies).
  `Sequence` is monotonic per run and repo-assigned; `task_message` events
  inherit that, which is what makes answer-after-question replay ordering
  work.
- Byte budgets precedent: `SubagentConfig.InlineOutputBytes` (default 4096)
  and `[tools] max_tool_result_bytes` / `batch_result_budget_bytes` threaded
  via `SessionDispatcherOpts.resultBudgets`
  (`internal/cli/dispatcher.go:93-100`).
- Bus subscribers today: the TUI `UIAdapter` and `events.MetricsAdapter`
  (both wired in `internal/cli/tui_run.go`); a new event kind reaches the
  metrics adapter too.
- Durable ID conventions: run/holder IDs are crypto/rand + base32
  (`newRunID`, `internal/coordinator/types.go:109`) - load-bearing for
  INV-AG-9 unguessability. Attempt IDs (`newAttemptID`,
  `types.go:202`) are a **process-local sequential counter** and must not
  be imitated for anything durable. Content refs are sha256 via
  `internal/contentref` (`"ref:<kind>:<64 hex>"`), with a fixed kind
  registry (`KindOutput`/`KindError`; unknown kinds resolve to `""`).

## 3. Design

### 3.1 Envelope

New package `internal/agentmsg` (name open, §6):

```go
type Kind string

const (
    KindFinding  Kind = "finding"  // child→parent, durable, blackboard
    KindQuestion Kind = "question" // child→parent, blocking (02)
    KindAnswer   Kind = "answer"   // reply to question/ask; parent- or router-routed (03, 04)
    KindSteer    Kind = "steer"    // parent→child (03)
    KindAsk      Kind = "ask"      // peer via parent router (04)
)

// Progress/heartbeat stays on the existing agent.EventKind stream and is
// deliberately NOT an envelope kind (see §3.2).

type Message struct {
    ID        string    // ULID, minted once at send
    RunID     string
    Kind      Kind
    From      Party     // {TaskID, Agent, Role} or parent sentinel
    To        Party     // parent sentinel, task, or role (04)
    InReplyTo string    // message ID; required for answer
    Body      string    // bounded; oversize → ledger ref + synopsis
    Refs      []string  // ledger content refs (recorded, never re-minted)
    CreatedAt time.Time
}
```

`ID` follows the `newRunID` convention: crypto/rand + base32
(`msg-<base32>`), NOT the process-local `attempt-%d` counter, which would
collide across restarts for durable, replayed IDs.

Validation is strict: unknown kinds rejected, `Body` length enforced at
construction, `Refs` must be well-formed ledger refs of an allowed kind.
This phase registers a new `contentref` kind (`KindMessage`) - the kind
registry in `internal/contentref` is closed (unknown kinds render as `""`),
so message refs are illegal until registered there and in every ref-kind
validation site. The A2A mapping
(message → A2A `Message`, finding → `Artifact`, question → `input-required`
status update) is documented in the package doc but not implemented.

### 3.2 Transport, in order of authority

1. **Ledger is the source of truth.** Every non-`progress` message is
   persisted as a content-addressed ledger entry under its run before any
   in-memory delivery. Crash/recovery replays from the ledger, mirroring
   `salvageUnjoinedRun` (`internal/cli/orchestrate_salvage.go`).
   **Retention rule:** message content refs are pinned for the life of
   their run's lifecycle events - ledger content GC must never leave a
   `task_message` event whose ref no longer resolves (INV-AG-10's
   resolution guarantee), OR replay must tolerate `ErrContentNotFound` by
   degrading to the event's synopsis. Pick one in Step 0; the invariant
   "a ref handed to the model resolves" favors pinning.
2. **Lifecycle events announce.** A `ledger.LifecycleEvent` with
   `Kind: "task_message"` and the message ID + synopsis in `Payload`
   (bounded/redacted per its contract) is emitted. **Named gap:**
   `emitLifecycleEvent` (`internal/coordinator/types.go:164`) is reachable
   only from inside the coordinator; a child's tool handler (cli side) has
   no path to it today. This phase adds the seam - a coordinator API
   (`PostTaskMessage(runID, taskID, msg)`) that persists, appends the
   event, and emits to subscribers - rather than letting cli write ledger
   rows directly with no live announce.
3. **UI bus mirrors.** A new `agent.EventKind` (`EventSubagentMessage`) is
   forwarded through the switch in `OnEventForMultiStep`
   (`internal/cli/dispatcher.go:386-410`), which currently drops unknown
   kinds, with a corresponding `events.Kind` for the bus/TUI adapter
   (these are two distinct enums; do not conflate). Coordinate with plan
   45, which edits the same switch. The metrics adapter will also observe
   the new kind.

`progress` is not carried by this envelope at all: it already exists as
`agent.EventKind` heartbeat/tool events and stays UI-only. Keeping a
`KindProgress` in the enum would create a second representation of the
same thing with no conversion, so the envelope vocabulary starts at
`finding` (§ was `progress` in an earlier draft; dropped).

### 3.3 Budgets and config

Extend `config.SubagentConfig` (`internal/config/types.go:269`):

```toml
[subagents.messaging]
enabled = true               # on by default (product decision, 2026-08-02);
                             # flag exists as an operational kill switch only
max_body_bytes = 2048        # per message, inline
max_messages_per_task = 32   # child upstream send quota per attempt (all kinds)
mailbox_capacity = 32        # parent→child mailbox depth (03) - separate knob
max_pending_questions = 1    # per task; one pot shared by question (02) and
                             # blocking ask (04); non-blocking asks don't
                             # consume it but count against max_asks_per_task
```

Kind direction annotations above are routing policy, not envelope
structure - `answer` is emitted by whoever holds the pending question
(parent in 03, target child in 04). Note the config polarity: existing
integrations use `disable = false` (e.g. tavily); `enabled` here is a
deliberate exception because it names a capability, not an integration -
flagged for Step 0 style review.

Defaults registered in `internal/config/defaults.go`; loading covered in
`load_test.go` style table tests. Messaging is **enabled by default**: this
phase still ships no observable behavior (nothing consumes messages until
`02`), but once `02`/`03` land their features are on out of the box.
`enabled=false` is a kill switch that makes send paths refuse cleanly, not
a rollout gate.

### 3.4 What is deliberately absent

- No delivery, no mailbox, no tools - those are `02`/`03`.
- No message mutation or deletion: append-only, like the ledger.
- No priority levels; kinds are the only routing signal.

## 4. Fingerprint safety (hard requirement)

Nothing in this phase touches `subagents.Task` or `runtime.Request`.
The fingerprint is an explicit allowlist projection (`fingerprintTask`,
`internal/coordinator/spawn.go:102-118`), and `requestFingerprint` is
unexported - so the pin is a golden-value test in package `coordinator`:
pin `requestFingerprint(fixedTasks)` (or `RunSnapshot.RequestFingerprint`
via `Spawn`+`Inspect` on a memory repo) to a hard-coded sha256 literal.
Any later member that widens `fingerprintTask` fails the pin and must
consciously update it.

## 5. Verification

- Unit: envelope validation table tests; budget enforcement; ledger
  round-trip (write → replay); lifecycle event emission with payload ref.
- `make verify` clean; no TUI change observable with `enabled=false`.
- Fingerprint pin test as above.

## 6. Open decisions

1. Package name: `internal/agentmsg` vs folding into `internal/subagents`.
   Position: separate package, and a **leaf** like `internal/contentref` -
   it may import only stdlib + `contentref`, never `ledger` (if `ledger`
   ever needs the `Message` type while `agentmsg` imports `ledger`, that
   is a cycle; serialization into `LifecycleEvent.Payload` happens in
   coordinator/cli).
2. Pinning vs tolerate-missing for message content refs under ledger GC
   (§3.2 transport rule) - must be closed at Step 0.
3. RESOLVED by validation: message IDs use the `newRunID` crypto/rand +
   base32 convention (`msg-<base32>`). The coordinator's `attempt-%d`
   counter is process-local and unsuitable for durable IDs; no ULID
   precedent exists in the codebase.
