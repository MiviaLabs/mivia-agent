# 53.03 - Parent→child: mailbox, Steer, Answer

**Status:** DESIGN - ADLC Step 0 not run.
**Date:** 2026-08-02
**Part of:** program `53` (`00-overview.md`).
**Depends on:** `01` (envelope), `02` (question state; `Answer` targets it).
**Blast radius:** HIGH - injects parent-authored content into a running
child's context; touches `RunHandle`, the multi-step handler,
**`internal/agent` itself** (the loop must grow an injection hook - see
§3.2), `internal/subagents` (pool context plumbing), and adds a session
tool.

## 1. Goal

Give the parent a post-spawn channel to a running child:

- **`Answer`** - reply to a `02` `Question`, unblocking the parked child.
- **`Steer`** - unsolicited mid-task guidance ("stop expanding scope, the
  answer is in internal/coordinator only"), delivered at the child's next
  step boundary.

Anthropic shipped their multi-agent system *without* steering and called
async steering a state-consistency risk. This member keeps semantics
minimal on purpose: at-most-once-per-step delivery, step-boundary only,
never interrupts a tool in flight, and drops cleanly if the child finishes
first.

## 2. Verified baseline

- `subagents.Task` / `runtime.Request` are value structs copied at spawn.
  The fingerprint is an explicit **allowlist projection**
  (`fingerprintTask`, `internal/coordinator/spawn.go:102-118`), so a
  non-fingerprinted `Task` field would not break the pin - the
  context-only stance in §3.1 is a design choice (keep `Task` a pure work
  description), not a necessity. Note `runtime.Caller`
  (`internal/runtime/context.go:11`, `ContextWithCaller` at `:39`) is
  stamped by the **dispatcher**, not the coordinator - it is a precedent
  for context-carried metadata, not the same insertion point.
- `MultiStepHandler.run` (`internal/subagents/multi_step.go:105-170`) makes
  **one blocking call** into `agent.Loop.Run`; the step loop, the
  `Messages` slice (`internal/agent/loop.go:87`), and `emitStep` all live
  inside `agent.Loop.Run` (`loop.go:111-157`). The handler has no code
  between steps; `opts.OnEvent` (`multi_step.go:158-165`) is a
  notification sink with no mutation contract. Its per-run companion
  goroutine (`emitHeartbeat`, `:299`) shows the async-companion pattern.
- `RunHandle` (`internal/coordinator/types.go:18`) is what the parent-side
  tools resolve via `runHandles` (`internal/cli/orchestration_state.go:28`);
  `cancel_run` / `join_run` registration in `registerOrchestrationTools`
  (`internal/cli/orchestrate.go:410`) is the template for a sibling tool.
  Its two mutexes have documented disciplines (`h.mu` for result/cancel
  state; `attemptsMu` single-writer, `types.go:37-44`) - neither covers a
  mailbox's three concurrent parties.
- Model-visible injected content precedent: hook output framing
  (`agent.FrameHookOutput`, `internal/agent/hook_context.go:79`, with
  `NeutralizeHookTags`-style forged-tag neutralization at `:88-100`).
  Steer text reuses the *pattern* - paired delimiter tags + a dedicated
  neutralizer - with its own `<parent-message>` tag pair, **not**
  `FrameHookOutput` itself, whose frame identifies content as
  lifecycle-hook output.

## 3. Design

### 3.1 Mailbox plumbing

- `RunHandle` gains `mailboxes map[TaskID]*taskMailbox`, where
  `taskMailbox` holds a bounded channel (cap = `mailbox_capacity`, `01`)
  plus a `terminal` flag, all guarded by a **new dedicated
  `mailboxesMu`** - neither `h.mu` nor the single-writer `attemptsMu`
  discipline covers the three concurrent parties (parent tool goroutine
  sends/creates, child loop drains, DAG goroutine marks terminal). On task
  terminal the DAG goroutine sets `terminal` under the mutex; the channel
  is **never `close()`d** - close-on-terminal both panics under the
  send/terminal race and kills the mailbox for retry attempts (terminal
  CAS is per attempt; a `failed`/`timed_out` task can be re-queued with a
  fresh attempt, `internal/coordinator/dag.go:274`, recovered-retry at
  `:166-179`). On retry, the coordinator drains and reseeds the mailbox
  for the new attempt from the ledger's undelivered records.
- The receive end reaches the child via **context**, but not at
  `runtime.Caller`'s insertion point (the dispatcher stamps `Caller`; the
  coordinator never touches the child's context today). `subagents.Pool`
  gains an optional `ContextForTask func(ctx, taskID) context.Context`
  applied in `executeOne` when deriving `taskCtx`
  (`internal/subagents/subagents.go:248-270`); the coordinator supplies it
  at `pool.Run` time. Never via `Task`/`Request` fields - a choice (see
  §2), guarded by the `01` fingerprint pin regardless.
- Persist-then-deliver, per `01`: `send_to_task` writes the message to the
  ledger first; the channel is best-effort delivery of an already-durable
  fact. **Replay is coordinator-side** - `MultiStepHandler` deliberately
  has no ledger repository (`multi_step.go:230-236`). At attempt dispatch
  (including retries and `ResumeInterruptedRun`,
  `internal/coordinator/recovery.go:82`, which rebuilds handles - and
  therefore mailboxes - from the ledger, since in-memory mailboxes do not
  survive the process), the coordinator seeds the mailbox with undelivered
  messages before the child's first step.

### 3.2 Delivery at the step boundary

Delivery happens **inside `agent.Loop.Run`**, which owns the step loop and
the message slice - `MultiStepHandler` has no code between steps, and
mutating loop state from the `OnEvent` notification sink would be an
unordered side-channel that breaks the moment event delivery goes async.
So `agent.Options` gains a first-class hook:
`BeforeStep func() []provider.Message` (nil = no-op), called on the loop
goroutine at the top of each iteration, **before history pruning and
request build** (so `PruneMessagesKeepTurns` sees injected frames as part
of a complete turn); returned messages are appended to `l.Messages`.
`MultiStepHandler.loopOptions` wires it to the mailbox drain. Per
delivery:

1. Non-blocking drain of the mailbox (all pending, in order).
2. `answer` messages route to the parked question's answer channel (`02`);
   a dangling answer (no pending question) degrades to a `steer`.
3. `steer` bodies are appended to the child's message list as a single
   framed user-role message:
   `<parent-message>…</parent-message>` with forged tag attempts in the
   body neutralized by a dedicated neutralizer (the `NeutralizeHookTags`
   pattern, not the hook helper itself). One frame per step
   even if multiple steers queued (concatenated), so a chatty parent cannot
   flood the child's context.
4. Delivered messages get a lifecycle event (`task_message_delivered`) so
   the ledger records receipt distinct from send.

A blocked-on-question child is *not* at a step boundary; its handler's
select in `02` step 3 additionally listens on the mailbox so `answer`
(and `cancel`) reach it while parked. `steer` received while parked is
held for the next real step.

### 3.3 Parent-side tool: `send_to_task`

Session-registry only (`registerSessionTool` path), never spawned scope.
Schema: `{run_id, task_id, kind: "steer"|"answer", body, in_reply_to?}`.
Gated by `orchestrationHandleAccessible`; refuses on terminal tasks with a
structured error naming the terminal status - **best-effort only** (the
read races the task finishing; the durable undelivered record in §3.4 is
the truth). Same budgets as `01`.

The interactive-user path from `02` (TUI answering a question) is rebuilt on
this same send seam - one code path for "answer arrives," regardless of
author.

### 3.4 Failure semantics

- Send to a task that finishes before delivery: message stays in the
  ledger, marked undelivered; `run_messages` shows it; no error to the
  sender beyond a `delivered: false` in the tool result when knowable.
- Full mailbox: send fails fast with a quota error - backpressure is the
  parent's problem, by design.
- No delivery ordering guarantees across tasks; per-task order is channel
  order.

## 4. Invariants

- Fingerprint pin test unchanged (mailbox via context only).
- INV-AG-9 on `send_to_task`; principal must match the run.
- Framing: injected steer text goes through the shared framing helper -
  no new ad-hoc model-visible envelope formats.
- Heartbeat rules: mailbox drain must not add a blocking wait at the step
  boundary (drain is non-blocking; parked-select already governed by `02`).

## 5. Verification

- Unit: mailbox lifecycle (lazy create, bounded, terminal-flag rejection,
  send-vs-terminal race does not panic, retry reseed); answer→question
  routing incl. dangling answer; frame neutralization of hostile bodies;
  `BeforeStep` ordering vs pruning (injected frame survives
  `PruneMessagesKeepTurns`).
- Integration: steer mid-run alters child behavior (fixture agent echoes
  received frames); answer unblocks parked child; message-before-finish
  race leaves durable undelivered record; recovery replay delivers
  post-crash.
- Concurrency: `-race` over send/deliver/cancel interleavings; a test in
  the spirit of `internal/cli/dispatch_timeout_test.go` for
  steer-vs-deadline races.

## 6. Open decisions

1. **Role of the injected message**: user-role frame (matches hook-output
   precedent) vs a synthetic tool-result. Position: user-role frame -
   validated as wire-safe: at the top of a step, history ends with the
   previous batch's tool results, and the OpenAI-compat format has no
   strict role alternation; the loop's pairing/pruning guards
   (`loop.go:178-186`, `:256`) are unaffected. Accepted caveat: later
   compaction may drop a steer frame like any other old message.
2. **Steer availability.** Per the program-level default-on decision
   (2026-08-02), `steer` ships enabled with the rest of messaging; a
   dedicated kill switch (`messaging.allow_steer`, default true) remains so
   it can be disabled independently of `answer` (which `02`'s question flow
   requires) if it proves harmful in practice.
3. Whether `send_to_task` supports broadcast (`task_id: "*"`). Position:
   no - iterate explicitly; broadcast invites the chat-room failure mode.
