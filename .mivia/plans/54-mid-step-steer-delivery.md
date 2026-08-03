# 54 - Mid-step steer delivery (interruptible steers + watchdog)

**Status:** DESIGN - ADLC Step 0 CHALLENGED (3 hostile reviews) and LOCKED.
**Date:** 2026-08-03 (rev 2 after Step 0 disposition)
**Part of:** program `53` follow-up (extend `03-parent-to-child.md`).
**Depends on:** `53.03` (mailbox, `BeforeStep`, `send_to_task`), `53.01` (envelope).
**Blast radius:** MEDIUM-HIGH - touches `internal/agent` (the step loop), `internal/coordinator` (mailbox signal), `internal/runtime` (context plumbing), `internal/subagents` (wiring), `internal/agentmsg` (envelope flag), `internal/cli` (`send_to_task` schema + two `MultiStepHandler` construction sites), `internal/config` (watchdog interval).

## 1. Thesis

Plan 53.03 delivers `Steer` **only at step boundaries**: `agent.Options.BeforeStep`
(`internal/agent/loop.go:82-85`, called from `prepareStep`,
`internal/agent/context.go:15-19`) drains the parent mailbox once per loop
iteration, i.e. **before each LLM call**. A "step" is one LLM call + one tool
batch (`loop.go:134` `for step := 1; ; step++` → `runStep` → `requestStep` +
`processToolCalls`). Because `MaxSteps=0` means unlimited steps and an LLM call
can run up to `DefaultRequestTimeout` (15 min, `internal/chat/binding.go:52`),
**a steer can wait hours** while a child grinds through long steps. Tool batches
are already bounded by `ToolTimeout`, so the LLM call is the unbounded term.

Goal: bound steer latency to a **configurable watchdog interval** (default 5 min)
and make **urgent steers land immediately** by softly interrupting the in-flight
LLM call - while *never* interrupting a tool in flight (side-effect safety) and
*never* changing the hard-cancel path (turn death).

## 2. Verified baseline

- Delivery seam: `BeforeStep func() []provider.Message` (`context.go:15-19`);
  wired in `MultiStepHandler.loopOptions` (`internal/subagents/multi_step.go:152-156`)
  from `coordinatorMailboxDrain(callCtx)` → `runtime.MailboxDrainFrom`
  (`internal/runtime/task_identity.go:49`); framed by `parentMessageBeforeStep`
  (`internal/subagents/parent_inject.go:22`) into `<parent-message>` frames.
- Context plumbing: `contextForTask` (`internal/coordinator/task_context.go:37-69`)
  stamps `TaskIdentity` and the mailbox drain closure via
  `runtime.ContextWithMailboxDrain`.
- Mailbox: `taskMailbox{ch chan agentmsg.Message, terminal bool}` guarded by
  `mailboxesMu` (`internal/coordinator/mailbox.go:10-15`); `Send` fails on
  terminal/full (`:41-54`); `Drain` non-blocking in order (`:57`); **channel
  never closed**; mailboxes are **lazily created** (`getOrCreate`, `:31-35`) and
  `Reseed` (`:118-132`) replaces the mailbox struct wholesale - **`Reseed` has
  no callers today** (verified; recovery does not replay messages).
- Loop interrupt handling: `requestStep` (`loop.go:342-370`) calls
  `l.Completer.ChatTurn(heartbeat, req)`; on error `runStep` (`loop.go:305-313`)
  treats `context.Canceled`/`DeadlineExceeded` as interrupted, preserves partial
  stream via `recordInterruptedPartial` (`loop.go:231`), and **returns the error**
  → `Loop.Run` returns → the child's turn DIES. Hard cancel only.
  **Provider timeout:** `internal/provider/openai_compat.go:224-229` applies
  `req.Timeout` on a **derived** context, so a request timeout yields
  `DeadlineExceeded` with the **turn ctx still alive** - the discriminator must
  therefore never be `ctx.Err()==nil` (see Step 0, AR-1/Defect 1).
- `MultiStepHandler` has **no `SubagentConfig` field** (`multi_step.go:13-70`);
  the three production construction sites are `internal/cli/dispatcher_handlers.go:58,117`
  and `internal/cli/agent_task_handler.go:196`. `OneShotHandler.Invoke`
  (`internal/subagents/oneshot.go`) never drains - "one-shot unchanged" is verified.
- Envelope: fixed 5-kind vocabulary (`internal/agentmsg/message.go:18-24`);
  `Message{ID, RunID, Kind, From, To, InReplyTo, Body, Refs, CreatedAt}`
  (`:79-89`); `NewMessage(runID, kind, from, to, body, refs, opts)` with
  `Options{MaxBodyBytes, Now, ID, InReplyTo}` (`:92-101`). The full envelope
  JSON round-trips the ledger (`coordinator/post_message.go:104-110`,
  `list_messages.go:79-93`), so a new optional field survives persistence and
  old rows decode inert.
- Config: `[subagents.messaging]` → `MessagingConfig` (defaults in
  `internal/config/defaults.go:37-52`; resolution `load.go:382-415`, which
  **zero-fills unset fields with defaults** - so an off-switch needs the `*int`
  pattern used by `ChatConfig.MaxSteps`, `types.go`).
- `send_to_task` tool: `internal/cli/send_to_task_tool.go`, schema
  `{run_id, task_id|task_ids, kind: steer|answer, body, in_reply_to?}`, gated by
  `accessibleOrchestrationHandle` (INV-AG-9). Broadcast path constructs messages
  at `send_to_task_tool.go:123-162` (`NewMessage` at `:131`) - a second
  construction site that must carry `Interrupt`.

## 3. Step 0 disposition (3 hostile reviews: architecture, correctness, concurrency)

All findings dispositioned; the plan below incorporates them. **Confirmed →
adopted:** AR-1/Defect 1 (sentinel-based soft interrupt - HIGH), Defect 2 + AR-4
(drop drain-side clear; `Pending()` gates BOTH watcher paths - resolves the
stale-signal race structurally), Defect 3 (cooldown + step accounting), AR-2 +
Defect 4 (interrupt channel is a per-call resolver; drop live-Reseed claims),
Defect 5 (partial text survives into `lastText`), AR-5 (`*int` watchdog config;
honest scorecard), AR-7 (wiring files + tool-not-canceled + timeout tests),
AR-3 (broadcast pass-through). **Rejected:** Options.StepInterrupt interface
(over-engineering, AR-6), cutting the watchdog (reverses the latency-bound goal,
AR-8), watchdog as a bare constant (operator SLA needs an off switch, AR-5).

## 4. Locked design

### 4.1 Envelope: `Interrupt` flag on `steer`

- `agentmsg.Message` gains `Interrupt bool` (`json:"interrupt,omitempty"`);
  `agentmsg.Options` gains `Interrupt bool`; `NewMessage` stamps it.
- `Validate`: `Interrupt == true` is valid **only** for `Kind == KindSteer`.
  Any other kind with `Interrupt` → `ErrInvalidMessage`.
- Persisted with the full envelope; old rows round-trip as `false` (inert).
- **Both** `send_to_task` construction paths (single + broadcast) pass it.

### 4.2 Mailbox: interrupt signal + pending check (no drain-side clear)

- `taskMailbox` gains `interruptCh chan struct{}` (buffered 1). `Send` enqueues
  the message first, then - still under `mailboxesMu` - non-blockingly signals
  `interruptCh` when `msg.Interrupt`. Signal-after-enqueue ordering is part of
  the lock discipline (a watcher can never see a signal with no message).
- `runMailboxes` exposes, both taking `mailboxesMu`:
  - `InterruptCh(taskID) <-chan struct{}` (getOrCreate; returns the live channel),
  - `Pending(taskID) bool` (non-consuming `len(ch) > 0`).
- **No drain-side clear.** The stale-signal case is closed structurally: the
  watcher cancels only when a signal fires **and** `Pending()` is true. A stale
  signal after a drain has `Pending()==false` → no cancel. A just-arrived signal
  always has its message pending → cancel is the goal. This removes the
  `Send`-vs-clear race (Defect 2) and leaves `parent_inject.go` untouched.
- Context: replace `ContextWithMailboxDrain` with a bundled
  `MailboxAccess{Drain MailboxDrainFunc; Interrupt func() <-chan struct{}; Pending func() bool}`
  under one key in `internal/runtime/task_identity.go`. `Interrupt` and `Pending`
  are **resolvers**, not values (lazy mailbox creation + potential future
  `Reseed` replacement; the child must observe the live channel). Keep
  `MailboxDrainFrom` as a thin bundle reader (two existing test readers depend
  on it: `ask_audit_fix_test.go:578`, `ask_integration_test.go:218`).
  Update the setter (`coordinator/task_context.go:55`) and the single reader
  (`subagents/parent_inject.go:58`).

### 4.3 Loop: soft interrupt of the LLM call only, sentinel-marked

- `agent.Options` gains:
  - `InterruptCh func() <-chan struct{}` (nil = off; resolver, re-read per call),
  - `MailboxPending func() bool` (nil = off),
  - `WatchdogInterval time.Duration` (0 = off),
  - `SoftInterruptCooldown time.Duration` (default 5s; 0 = off for tests).
- `requestStep` derives an **LLM-scoped context** `llmCtx, llmCancel :=
  context.WithCancel(ctx)` around the `ChatTurn` call (keep the existing
  `heartbeat` for model-thinking progress). Tools keep the turn `ctx` - **only
  `llmCtx` is cancelable by a steer** (verified: `processToolCalls` →
  `runToolBatch` → `executeToolsParallel` run on the turn ctx).
- A per-call watcher goroutine (started in `requestStep`, exited via `defer
  llmCancel()` → `<-llmCtx.Done()` and `<-ctx.Done()` branches; **never leaks**)
  loops selecting on:
  - `InterruptCh()` signal AND `MailboxPending() == true` AND
    `time.Since(lastSoftInterrupt) >= SoftInterruptCooldown` AND `ctx.Err()==nil`
    → `llmCancel()`; record `lastSoftInterrupt = now`,
  - watchdog tick AND `MailboxPending() == true` AND cooldown satisfied AND
    `ctx.Err()==nil` → `llmCancel()`; record `lastSoftInterrupt = now`,
  - `llmCtx.Done()` → exit (call ended),
  - `ctx.Done()` → exit (hard cancel; do nothing).
  The watcher lives only during the LLM call, so a steer arriving during a tool
  batch is **never** a cancel - it lands at the next `BeforeStep`.
- **Sentinel, not inference:** `requestStep` returns `errSteerInterrupt`
  (a package sentinel) when *its own watcher* canceled. `runStep` soft-continues
  **only** on `errors.Is(err, errSteerInterrupt)`: preserve partial
  (`recordInterruptedPartial`), carry the partial into `lastText` (via the
  returned outcome text, so a later tool-only/empty reply still surfaces the
  partial - Defect 5), and return `stepOutcome{}, nil` → the loop iterates →
  `BeforeStep` drains the steer → the next LLM call sees it. **Every other
  error - `DeadlineExceeded` (provider timeout), unmarked `Canceled`, any
  provider error - keeps today's propagate-and-abort semantics** (Defect 1).
  Hard cancel (turn ctx canceled) is unchanged.
- **Step accounting + cooldown (Defect 3):** each soft interrupt consumes one
  loop iteration (a step / `stepCount` / schema-retry budget unit). The cooldown
  (default 5s) caps interrupt frequency so a flooding parent cannot starve the
  child or burn `MaxSteps`/`NestedSteps` faster than one interrupt per cooldown.
  Documented; flood-tested.
- **Watchdog cost:** fires only when `Pending()` is true, so idle children pay
  nothing. A soft interrupt abandons the in-flight LLM generation (one wasted
  round-trip; nothing preserved for non-streaming subagents) - accepted and
  documented: the operator asked to break in.
- Scope note: applies to **multi-step agents** only (the only loop that drains
  today); one-shot tasks keep current behavior.

### 4.4 `send_to_task` schema

- Schema gains `"interrupt": {"type": "boolean"}`; `sendToTaskParams` gains
  `Interrupt bool`. Validation: `interrupt == true` requires `kind == "steer"`
  (structured error otherwise). Passed into `agentmsg.Options.Interrupt` on both
  the single and broadcast paths. `Description()` updated.

### 4.5 Config: watchdog interval (off-switch via `*int`)

- `MessagingConfig` gains `SteerWatchdogSeconds *int`
  (`toml:"steer_watchdog_seconds"` under `[subagents.messaging]`): `nil` →
  default 300; explicit `0` → disabled; else the value. `resolveMessagingConfig`
  resolves `nil` to 300 and preserves explicit 0 (the repo's `*int` pattern).
- `MultiStepHandler` gains a field (e.g. `SteerWatchdog time.Duration`, fed at
  all three construction sites: `dispatcher_handlers.go:58,117`,
  `agent_task_handler.go:196`) → `Options.WatchdogInterval`.
- Honest scorecard note: default 300 **changes** behavior for existing
  deployments (steers become bounded at 5 min instead of unbounded) - that is
  the plan's point, stated as such.

## 5. Invariants this plan must not break

- **Fingerprint stability (INV via `coordinator/spawn.go`):** no new `Task`
  fields; everything travels via context/Options like the existing drain.
- **Principal scoping (INV-AG-9):** `send_to_task` stays gated; the new param is
  inside the existing tool.
- **Never interrupt a tool:** the only cancelable context is `llmCtx`, scoped to
  `ChatTurn`; tool batches keep the turn ctx. Safety-tested.
- **Hard cancel / timeout unchanged:** turn-ctx cancel and provider
  `DeadlineExceeded` still abort the child exactly as today. Tested.
- **Concurrency/heartbeat rules** (`.mivia/rules/50`, `.mivia/rules/70`): the
  watcher is per-LLM-call, exits on `llmCtx.Done()`/`ctx.Done()`, no shared
  mutation, cooldown serializes cancels.
- **Byte discipline / fixed vocabulary:** `Interrupt` is a bool field, not body
  content; budgets unchanged.
- **Stale-signal hygiene:** `Pending()` gates both watcher paths; no drain-side
  clear exists to race with `Send`. `-race`-tested.

## 6. Files

| File | Change |
|------|--------|
| `internal/agentmsg/message.go` (+`_test.go`) | `Message.Interrupt`, `Options.Interrupt`, validation (steer-only) |
| `internal/runtime/task_identity.go` (+`_test.go`) | `MailboxAccess{Drain, Interrupt func() <-chan struct{}, Pending func() bool}` bundle + key; keep `MailboxDrainFrom` reader |
| `internal/coordinator/mailbox.go` (+`_test.go`) | `taskMailbox.interruptCh`, signal-after-enqueue in `Send`, `InterruptCh(taskID)`, `Pending(taskID)` (locked) |
| `internal/coordinator/task_context.go` | set the `MailboxAccess` bundle (drain + interrupt resolver + pending resolver) |
| `internal/subagents/multi_step.go` | read the bundle; feed `Options.InterruptCh`/`MailboxPending`/`WatchdogInterval`/`SoftInterruptCooldown`; new handler field |
| `internal/cli/dispatcher_handlers.go`, `internal/cli/agent_task_handler.go` | feed the new `MultiStepHandler` watchdog field at all three construction sites |
| `internal/agent/loop.go`, `internal/agent/context.go` (+`_test.go`) | `Options` fields; `llmCtx` in `requestStep`; watcher with cooldown; `errSteerInterrupt` sentinel; soft-continue only on sentinel; partial → `lastText` |
| `internal/cli/send_to_task_tool.go` (+`_test.go`) | `interrupt` param, validation, pass-through (single + broadcast), description |
| `internal/config/defaults.go`, `types.go`/`load.go` (+`_test.go`) | `SteerWatchdogSeconds *int` (nil→300, 0 off) |
| Integration (e2e) test | blocking-completer subagent; interrupt steer lands promptly; non-interrupt steer lands at boundary (regression) |

`parent_inject.go` is **not** modified (no drain-side clear in the locked design).

## 7. Test strategy (TDD, named)

- `agentmsg`: `TestMessageInterruptFlagSteerOnly` (steer ok; finding/answer/ask/question + Interrupt → invalid); `TestMessageInterruptRoundTripsLedgerJSON` (encode/decode preserves; old row decodes false).
- `runtime`: `TestMailboxAccessContextRoundTrip` (set/get bundle; nil-safe readers).
- `coordinator/mailbox`: `TestMailboxSendSignalsInterruptAfterEnqueue` (interrupt-flagged → signal AND message present; plain steer → no signal); `TestMailboxPendingReflectsQueued` (true while queued, false after drain); `TestMailboxAccessorsLocked` (interrupt/pending under `mailboxesMu`).
- `agent/loop` (scripted completer that blocks on `llmCtx` cancel; **handshake + return-value assertions, no negative timing assertions**):
  - `TestSoftInterruptContinuesLoop` (watcher cancel → sentinel → loop continues; steer injected next step; no error),
  - `TestRequestTimeoutStillAborts` (completer returns `DeadlineExceeded`, turn ctx alive → error propagates, child aborts - **regression for Defect 1**),
  - `TestHardCancelStillAborts` (turn ctx canceled → error returned, unchanged),
  - `TestSoftInterruptToolBatchNotCanceled` (interrupt signal during a slow tool batch → batch completes; steer lands at next boundary - **safety invariant**),
  - `TestSoftInterruptPartialSurvivesAsFinalText` (streaming; post-steer reply tool-only → final text carries the partial - Defect 5),
  - `TestWatchdogInterruptsOnlyWhenPending` (Pending true → interrupt; false → completer finishes untouched),
  - `TestSoftInterruptCooldownCapsFlood` (N urgent signals in < cooldown → at most one cancel; no starvation),
  - `TestStaleSignalNoCancelWithoutPending` (signal consumed, Pending false → next LLM call not canceled),
  - `TestInterruptChNilNoOp`, `TestWatchdogZeroDisabled`, `TestWatcherExitsOnCallEnd` (no leak; `-race`).
- `subagents`: `TestMailboxAccessBundleWiring` (bundle resolvers reach `loopOptions`; one-shot handler unaffected).
- `cli/send_to_task`: `TestSendToTaskInterruptValidation` (answer+interrupt rejected; steer+interrupt ok); `TestSendToTaskInterruptBroadcast` (both targets get the flag); e2e `TestSendToTaskInterruptBreaksIntoBlockedChild` (blocking completer; interrupt steer lands promptly; child completes with steer visible); `TestSteerLandsAtStepBoundaryUnchanged` (regression).
- `config`: `TestMessagingConfigSteerWatchdogUnsetDefaults300`, `TestMessagingConfigSteerWatchdogZeroDisabled`.

## 8. Scorecard

| Criterion | PASS/FAIL |
|-----------|-----------|
| Compiles | PASS (pure additions; one bundle refactor with two call sites) |
| No cycles | PASS (runtime stays leaf; coordinator/subagents/agent already import runtime) |
| No breaking API | PASS (additive fields; `MailboxDrainFrom` kept) |
| Testable in isolation | PASS (scripted completer; no provider needed) |
| Backward-compatible config | PASS (`*int`: unset = today's unbounded behavior is replaced by default 300 - a deliberate, documented behavior change; explicit `0` restores unbounded) |
| Every function has a test | PASS (test table above) |

## 9. Rollback criterion

Plan is rejected if: (a) soft interrupt cannot be reliably distinguished from
provider timeout without touching the tool path (locked: sentinel fixes it); (b)
any path lets a steer cancel a tool in flight; (c) the stale-signal/Pending()
discipline is not race-free under `go test -race`; (d) the cooldown cannot bound
interrupt frequency. Rollback = revert the feature commit(s); the flag and
config default to inert (`Interrupt` false, watchdog `nil`/0) so a revert is
behavior-neutral.
