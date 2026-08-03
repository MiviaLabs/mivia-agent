# 54 - Mid-step steer delivery (interruptible steers + watchdog)

**Status:** DESIGN - ADLC Step 0 pending (hostile challenge runs after this file lands).
**Date:** 2026-08-03
**Part of:** program `53` follow-up (extend `03-parent-to-child.md`).
**Depends on:** `53.03` (mailbox, `BeforeStep`, `send_to_task`), `53.01` (envelope).
**Blast radius:** MEDIUM-HIGH - touches `internal/agent` (the step loop), `internal/coordinator` (mailbox signal), `internal/runtime` (context plumbing), `internal/subagents` (wiring), `internal/agentmsg` (envelope flag), `internal/cli` (`send_to_task` schema), `internal/config` (watchdog interval).

## 1. Thesis

Plan 53.03 delivers `Steer` **only at step boundaries**: `agent.Options.BeforeStep`
(`internal/agent/loop.go:82-85`, called from `prepareStep`,
`internal/agent/context.go:15-19`) drains the parent mailbox once per loop
iteration, i.e. **before each LLM call**. A "step" is one LLM call + one tool
batch (`loop.go:134` `for step := 1; ; step++` → `runStep` → `requestStep` +
`processToolCalls`). Because `MaxSteps=0` means unlimited steps and an LLM call
can run up to `DefaultRequestTimeout` (15 min, `internal/chat/binding.go:52`),
**a steer can wait hours** while a child grinds through long steps. Tool batches
are already bounded by `ToolTimeout` (default 60s agent / 900s subagent), so the
LLM call is the unbounded term.

Goal: bound steer latency to a **configurable watchdog interval** (default 5 min)
and make **urgent steers land immediately** by softly interrupting the in-flight
LLM call - while *never* interrupting a tool in flight (side-effect safety) and
*never* touching the hard-cancel path (turn death).

## 2. Verified baseline

- Delivery seam: `BeforeStep func() []provider.Message` appended to `l.Messages`
  before pruning/request build (`context.go:15-19`); wired in
  `MultiStepHandler.loopOptions` (`internal/subagents/multi_step.go:152-156`) from
  `coordinatorMailboxDrain(callCtx)` → `runtime.MailboxDrainFrom`
  (`internal/runtime/task_identity.go:49`); framed by `parentMessageBeforeStep`
  (`internal/subagents/parent_inject.go:22`) into `<parent-message>` frames.
- Context plumbing: `contextForTask` (`internal/coordinator/task_context.go:37-69`)
  stamps `TaskIdentity` and the mailbox drain closure via
  `runtime.ContextWithMailboxDrain`; the closure calls `mb.Drain(taskID)`.
- Mailbox: `taskMailbox{ch chan agentmsg.Message, terminal bool}` guarded by
  `mailboxesMu` (`internal/coordinator/mailbox.go:10-15`); `Send` fails on
  terminal/full (`:41-54`); `Drain` non-blocking in order (`:57`); **channel never
  closed**; `Reseed` replays undelivered ledger messages on retry/recovery
  (`:118-132`). `MailboxSend` (`:96`) is the parent→child delivery used by
  `send_to_task`.
- Loop interrupt handling: `requestStep` (`internal/agent/loop.go:342-370`) calls
  `l.Completer.ChatTurn(heartbeat, req)`; on error `runStep` (`loop.go:305-313`)
  treats `context.Canceled`/`DeadlineExceeded` as interrupted, preserves partial
  stream via `recordInterruptedPartial` (`loop.go:231`), and **returns the error**
  → `Loop.Run` returns → the child's turn DIES. Hard cancel only.
- Envelope: fixed 5-kind vocabulary (`internal/agentmsg/message.go:18-24`);
  `Message{ID, RunID, Kind, From, To, InReplyTo, Body, Refs, CreatedAt}`
  (`:79-89`); `NewMessage(runID, kind, from, to, body, refs, opts)` with
  `Options{MaxBodyBytes, Now, ID, InReplyTo}` (`:92-101`).
- Tool batch: `executeToolsParallel(ctx, ...)` (`loop_tools.go:204`) runs tools
  on the **turn ctx** - canceling the turn ctx aborts tools. Tools must never be
  canceled by a steer.
- Config: `[subagents.messaging]` → `MessagingConfig` (defaults in
  `internal/config/defaults.go:37-52`; resolution `load.go:382-415`).
- `send_to_task` tool: `internal/cli/send_to_task_tool.go`, schema
  `{run_id, task_id|task_ids, kind: steer|answer, body, in_reply_to?}`, gated by
  `accessibleOrchestrationHandle` (INV-AG-9).

## 3. Design

### 3.1 Envelope: `Interrupt` flag on `steer`

- `agentmsg.Message` gains `Interrupt bool` (`json:"interrupt,omitempty"`);
  `agentmsg.Options` gains `Interrupt bool`; `NewMessage` stamps it.
- Validation (`Validate`): `Interrupt == true` is valid **only** for
  `Kind == KindSteer`. Any other kind with `Interrupt` → `ErrInvalidMessage`.
- Rationale: the interrupt is a **delivery directive that must survive the
  ledger** (a persisted interrupt-steer replayed by `Reseed` on retry must still
  interrupt). It is content-neutral (not part of `Body`), so byte budgets and the
  fixed vocabulary are untouched.

### 3.2 Mailbox: interrupt signal + non-consuming pending check

- `taskMailbox` gains `interruptCh chan struct{}` (buffered 1, created with the
  mailbox). `Send` (and `Reseed`) additionally, when `msg.Interrupt`, do a
  **non-blocking** `select { case interruptCh <- struct{}{}: default: }` so the
  signal never blocks delivery.
- `runMailboxes` exposes:
  - `InterruptCh(taskID) <-chan struct{}` (buffered 1; same channel the child
    watches; a send with no watcher leaves the value buffered - consumed by the
    next watcher or cleared by the next drain),
  - `Pending(taskID) bool` - non-consuming `len(ch) > 0` (heuristic; benign race
    with `Send`).
- Context: replace the single `ContextWithMailboxDrain` with a bundled
  `MailboxAccess{Drain MailboxDrainFunc; Interrupt <-chan struct{}; Pending func() bool}`
  carried under one key in `internal/runtime/task_identity.go`. Update the two
  call sites: `coordinator/task_context.go:55` (setter) and
  `subagents/parent_inject.go:58` `coordinatorMailboxDrain` (reader; keep the
  name, read the bundle). `MailboxDrainFrom` remains as a thin reader of the
  bundle so nothing else breaks.
- **Stale-signal rule:** `parentMessageBeforeStep`'s drain must also **clear**
  `interruptCh` (non-blocking receive) after draining the mailbox, and the loop
  watcher (3.3) only cancels when the signal fires **and** `Pending()` is true.
  Together these prevent a stale signal from wasting the *next* LLM call after
  its steer was already delivered at the boundary.

### 3.3 Loop: soft interrupt of the LLM call only

- `agent.Options` gains three fields (all optional, nil/zero = off):
  - `InterruptCh <-chan struct{}`
  - `WatchdogInterval time.Duration` (0 = off)
  - `MailboxPending func() bool` (nil = off; used by the watchdog)
- `requestStep` derives an **LLM-scoped context**: `llmCtx, llmCancel :=
  context.WithCancel(ctx)` around the `ChatTurn` call (keep the existing
  `heartbeat` for model-thinking progress). Tools keep using the turn `ctx` -
  **only `llmCtx` is cancelable by a steer**.
- A per-call watcher goroutine (started in `requestStep`, stopped when the call
  returns, bounded by the existing heartbeat/concurrency rules) selects on:
  - `InterruptCh` (a value arrives AND `ctx.Err() == nil`) → `llmCancel()`,
  - watchdog tick (every `WatchdogInterval`) AND `MailboxPending() == true` AND
    `ctx.Err() == nil` → `llmCancel()`,
  - `ctx.Done()` → do nothing (hard cancel path already handles it).
  The watcher lives only during the LLM call, so a steer arriving during a tool
  batch is **never** a cancel - it lands at the next `BeforeStep`.
- **Soft vs hard:** in `runStep`/`Loop.Run`, an interrupted LLM error with the
  **turn ctx still alive** (`ctx.Err() == nil`) is a *soft interrupt*: preserve
  partial stream (`recordInterruptedPartial`), then **continue the loop**
  (`stepOutcome{}, nil`) instead of returning the error. The next iteration's
  `BeforeStep` drains the steer, the next LLM call sees it. Hard cancel
  (`ctx.Err() != nil`) keeps today's behavior (record partial, return error).
- Cost note: a soft interrupt abandons the in-flight LLM generation (one wasted
  round-trip; nothing preserved for non-streaming subagents - documented and
  accepted: the operator explicitly asked to break in). The watchdog fires only
  when messages are pending, so idle children pay nothing.
- Scope note: applies to **multi-step agents** (the only loop that drains today).
  One-shot tasks keep current behavior (no mid-flight delivery).

### 3.4 `send_to_task` schema

- Schema gains `"interrupt": {"type": "boolean"}`; `sendToTaskParams` gains
  `Interrupt bool`.
- Validation: `interrupt == true` requires `kind == "steer"` (reject with a
  structured error otherwise); ignored for `answer`.
- `Execute` passes `Interrupt` into the message (`agentmsg.Options.Interrupt`)
  so it is persisted and honored by `MailboxSend`/`Reseed`.
- `Description()` text updated: "interrupt: (steer only) break into a long
  in-flight LLM call instead of waiting for the next step boundary".

### 3.5 Config: watchdog interval

- `MessagingConfig` gains `SteerWatchdogSeconds int`
  (`toml:"steer_watchdog_seconds"` under `[subagents.messaging]`), default 300,
  `0` disables. `resolveMessagingConfig` fills zero with the default.
- `MultiStepHandler` reads it (via its `SubagentConfig`) into
  `Options.WatchdogInterval`.

## 4. Invariants this plan must not break

- **Fingerprint stability (INV via `coordinator/spawn.go`):** no new `Task`
  fields; everything travels via context/Options like the existing drain.
- **Principal scoping (INV-AG-9):** `send_to_task` stays gated; the new param is
  inside the existing tool.
- **Never interrupt a tool:** the only cancelable context is `llmCtx`, scoped to
  the `ChatTurn` call. Tool batches keep the turn ctx. Tested.
- **Hard cancel unchanged:** turn-ctx cancel still aborts the child and records
  partial output. Tested.
- **Concurrency/heartbeat rules** (`.mivia/rules/50-concurrency-subagents.md`,
  `.mivia/rules/70-long-running-heartbeat.md`): the watcher goroutine is
  per-LLM-call, stops on call return or `ctx.Done`, and does no shared mutation.
- **Byte discipline / fixed vocabulary:** `Interrupt` is a bool field, not body
  content; budgets unchanged.
- **Stale-signal hygiene:** a signal consumed by the drain must not waste the
  next LLM call (3.2 stale-signal rule). Tested.

## 5. Files

| File | Change |
|------|--------|
| `internal/agentmsg/message.go` (+`_test.go`) | `Message.Interrupt`, `Options.Interrupt`, validation (steer-only) |
| `internal/runtime/task_identity.go` (+`_test.go`) | `MailboxAccess{Drain, Interrupt, Pending}` bundle + context key; keep `MailboxDrainFrom` reader |
| `internal/coordinator/mailbox.go` (+`_test.go`) | `taskMailbox.interruptCh`, signal on `Send`/`Reseed`, `InterruptCh(taskID)`, `Pending(taskID)` |
| `internal/coordinator/task_context.go` | set the `MailboxAccess` bundle (drain + interrupt + pending) |
| `internal/subagents/parent_inject.go` (+`_test.go`) | read the bundle; clear stale interrupt signal on drain |
| `internal/subagents/multi_step.go` | wire `InterruptCh`/`WatchdogInterval`/`MailboxPending` into `Options` |
| `internal/agent/loop.go`, `internal/agent/context.go` (+`_test.go`) | `Options` fields; `llmCtx` in `requestStep`; watcher; soft-interrupt continuation |
| `internal/cli/send_to_task_tool.go` (+`_test.go`) | `interrupt` param, validation, pass-through, description |
| `internal/config/defaults.go`, `types.go`/`load.go` (+`_test.go`) | `SteerWatchdogSeconds` (default 300, 0 off) |
| Integration (e2e) test | blocking-completer subagent; interrupt steer lands promptly; non-interrupt steer lands at boundary (regression) |

## 6. Test strategy (TDD, named)

- `agentmsg`: `TestMessageInterruptFlagSteerOnly` (steer+parent ok; finding/answer/ask/question with Interrupt → invalid).
- `runtime`: `TestMailboxAccessContextRoundTrip` (set/get bundle; nil-safe readers).
- `coordinator/mailbox`: `TestMailboxSendSignalsInterrupt` (interrupt-flagged → signal; plain steer → no signal); `TestMailboxReseedSignalsInterrupt`; `TestMailboxPendingReflectsQueued` (and clears after drain).
- `agent/loop` (scripted completer that blocks on `llmCtx` cancel):
  - `TestSoftInterruptContinuesLoop` (turn ctx alive; LLM canceled by signal; loop continues; steer injected next step; no error returned),
  - `TestSoftInterruptPreservesPartialStreaming` (streaming tee → partial text in history),
  - `TestHardCancelStillAborts` (turn ctx canceled → error returned, unchanged),
  - `TestWatchdogInterruptsOnlyWhenPending` (Pending true → interrupt; false → completer finishes untouched),
  - `TestInterruptChNilNoOp` and `TestWatchdogZeroDisabled`,
  - `TestStaleSignalNotFiredAfterDrain` (signal consumed by drain; next LLM call not canceled).
- `subagents/parent_inject`: `TestParentInjectClearsInterruptSignalOnDrain`.
- `cli/send_to_task`: `TestSendToTaskInterruptValidation` (answer+interrupt rejected; steer+interrupt ok); e2e `TestSendToTaskInterruptBreaksIntoBlockedChild` (blocking completer; steer with interrupt lands promptly; child completes with steer visible in its context); `TestSteerLandsAtStepBoundaryUnchanged` (regression).
- `config`: `TestMessagingConfigSteerWatchdogDefault` (300 default; 0 preserved).

## 7. Scorecard

| Criterion | PASS/FAIL |
|-----------|-----------|
| Compiles | PASS (pure additions; one bundle refactor with two call sites) |
| No cycles | PASS (runtime ← coordinator/subagents/agent, existing direction) |
| No breaking API | PASS (additive fields; `MailboxDrainFrom` kept) |
| Testable in isolation | PASS (scripted completer; no provider needed) |
| Backward-compatible config | PASS (`steer_watchdog_seconds` default 300 = today's behavior for non-urgent steers bounded at 5 min; `0` restores unbounded) |
| Every function has a test | PASS (test table above) |

## 8. Rollback criterion

Plan is rejected if: (a) soft interrupt cannot be reliably distinguished from
hard cancel without touching the tool path; (b) any path lets a steer cancel a
tool in flight; (c) the stale-signal rule cannot be made race-free under
`go test -race`. Rollback = revert the feature commit(s); the flag and config
default to inert (`Interrupt` false, watchdog 0) so a revert is behavior-neutral.
