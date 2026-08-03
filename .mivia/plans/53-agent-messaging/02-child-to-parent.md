# 53.02 - Child→parent: Finding, Question, and the blackboard

**Status:** DESIGN - ADLC Step 0 not run.
**Date:** 2026-08-02
**Part of:** program `53` (`00-overview.md`).
**Depends on:** `01` (envelope, budgets, transport).
**Blast radius:** HIGH - a new model-visible upstream channel, a new child
tool, a new attempt state, and result-envelope changes.

## 1. Goal

Let a running subagent surface structured information upstream before its
final report:

- **`Finding`** - a durable discovery ("dispatcher.go:214 has a lock-order
  inversion; confidence medium") written to the run blackboard, visible to
  the parent and to later-spawned siblings, without interrupting anyone.
- **`Question`** - the A2A `input-required` shape: the child cannot proceed
  without input, parks, and is answered. Blocking, bounded,
  timeout-defaulted. **Answer-path reality (validated):** a parent blocked
  inside a synchronous `dispatch_tasks` call can never answer - it is
  waiting on the coordinator Join and cannot run tools. Parent answers
  (03) apply only to detached `spawn_agent` runs observed via `join_run`;
  in the synchronous path, questions resolve by user answer or timeout,
  permanently.

The parent model observes these by **pull only** - via result envelopes and
read tools - never by injection into an in-flight parent turn.

## 2. Verified baseline

- Child tool registries are `ScopeSpawned` allowlists built per invocation
  (`prepareInvokeSurface`, `internal/cli/agent_task_handler.go:126-136`,
  called from `Invoke` at `:98`); allowlist mode means a tool absent from
  `EffectiveTools` simply does not exist for the child, and
  `require_explicit_tools` (`internal/config/agents.go:31`) is
  deny-by-default. Adding a child-side tool therefore needs baseline
  injection (§3.1), not just registration.
- The final result is the parent's only model-visible signal:
  `dispatchTaskResult` (`internal/cli/dispatch.go:248-272`) /
  `modelTaskResult` (`internal/cli/orchestrate_lifecycle.go:21`), with
  `output_ref` + `synopsis` for oversize bodies and `ledger_read` for
  retrieval.
- `join_run` / `inspect_agents` (`internal/cli/orchestrate.go`,
  `orchestrate_lifecycle.go`) are the async observation surface for detached
  runs, principal-gated by `orchestrationHandleAccessible`
  (`internal/cli/orchestration_state.go:190`).
- **Task** status transitions are CAS-ed in the ledger via
  `CompareAndSetTaskStatus` under `ValidTaskTransition`
  (`internal/ledger/transition.go:13`); attempts are recorded separately
  via `SetTaskAttempt` and have **no** state machine. Status vocabulary:
  `queued / running / completed / failed / timed_out / canceled / blocked /
  cancel_requested / retry_pending` (`internal/ledger/types.go:36-44`) -
  note `blocked` already exists and is **terminal** (dependency failure;
  INV-AG-21 leans on it). Cancellation shows the pattern for a transitional
  status (`cancel_requested`, `internal/coordinator/cancel.go:66-73`).

## 3. Design

### 3.1 Child-side tool: `post_message`

One tool, kinds `finding` and `question`. Availability (per the program
default-on decision, 2026-08-02): injected as a **baseline messaging tool
before the per-agent allowlist filter** - spawned registries are
allowlist-mode (`ScopeSpawned` + `require_explicit_tools` deny-by-default),
so a plain registration would never reach any agent. An agent opts out via
`disallowed_tools = ["post_message"]` / `tools_remove`
(`internal/config/agents.go:53-55`). Schema:
`{kind, body, refs?, wait_seconds?}`, with `to_role` and `in_reply_to`
reserved (unused until 04). Enforcement at the tool layer: body budget,
per-task quota, `max_pending_questions`.

Provenance is stamped server-side from the invocation identity - the child
cannot spoof `From`.

### 3.2 Finding → blackboard

A `finding` is a `01` message persisted to the run ledger plus an entry in a
**blackboard index** for the run: `(run_id, seq, from, synopsis, ref)`.

Read paths:
- Parent: findings appended to the task's result envelope
  (`messages: [...]` field on `dispatchTaskResult` / `modelTaskResult`,
  synopsis-only, budget shared with the envelope) and a `run_messages` read
  tool (session-scoped, principal-gated) for full bodies via refs.
- Siblings: **at spawn only.** `buildSpawnTasks` /
  `runThroughCoordinator` may inject a bounded "blackboard so far" digest
  into a new task's brief when the caller sets `include_blackboard: true`.
  Running siblings do not get pushed findings - that is `03`/`04` territory
  and mostly rejected there too.

### 3.3 Question → blocked child

Flow:
1. Child calls `post_message{kind:"question", wait_seconds}`.
2. Message persists; the **task status** CAS-es `running → awaiting_input`
   (new status - `blocked` is taken and terminal, and `awaiting_input`
   matches A2A's `input-required`). `ValidTaskTransition` gains:
   `running → awaiting_input`; `awaiting_input → {running,
   cancel_requested, canceled, timed_out, failed}`. This is the codebase's
   **first return-to-`running` transition** and needs its own CAS race
   tests (answer vs cancel both CAS from `awaiting_input`; loser gets the
   conflict error and yields). `reconcileCancellation` must also learn the
   status - today it only CAS-es `queued`/`running → cancel_requested`
   (`cancel.go:66-67`), so a parked task would be skipped and left stuck.
   A lifecycle event announces the park; heartbeats continue per
   `.mivia/rules/70-long-running-heartbeat.md` (`emitHeartbeat` is an
   independent 30s ticker goroutine, so a blocked tool handler does not
   stall it).
3. The child's tool call **blocks** inside the handler on an answer channel
   (select: answer / task deadline / `wait_seconds` / ctx cancel). The
   worker slot stays occupied - accepted cost, see §6.2.
4. Answer arrives (via `03`'s `send_to_task` for detached runs, or from
   the user - §3.3a): tool returns the answer text as its result; CAS back
   to `running`.
5. Timeout: tool returns a structured `no_answer` result (`timed_out`
   reason in the *tool result*, not the termination vocabulary) and the
   child continues on its own judgment. Task-level termination reasons stay
   fixed-vocabulary; if the whole task dies while parked, its terminal
   reason is the existing timeout/cancel one, and the unanswered question
   remains readable in the ledger.

### 3.3a User-answer channel (sized honestly)

"Child asks the human," MCP-elicitation style, is the MVP - but it is
**not free**: the existing `Dispatcher.Sink` → `OnEventForMultiStep` → TUI
path is strictly one-way display; there is no user-input channel back into
a running tool handler and no interactive approval flow to reuse. 02 must
build: a TUI question modal/prompt fed by the new event kind, plus a
response channel keyed by `(run_id, task_id)` that routes into the parked
handler's select. This is its own work item within 02, not an assumed
capability. Headless (non-TUI) runs have no user channel: questions there
time out unless the run is detached and answered via `03`.

### 3.4 Wait semantics for blocked children

`dispatch_tasks`' batch timeout already bounds everything
(`dispatchOrchestrationSec`, `internal/cli/dispatch.go:49-73`) - and gives
the whole call only ~15s headroom over the longest task, so a child that
parks late in its budget eats its own task budget; `wait_seconds` must be
capped at the tool layer to the remaining attempt budget or the outer
deadline fires and INV-AG-21's salvage path is what returns results.
`wait_seconds` defaults low (60s). `inspect_agents` output marks parked
tasks so a polling parent can see who is waiting and why.

## 4. Invariants

- INV-AG-9: `run_messages` gated identically to `join_run`.
- INV-AG-10: refs recorded from ledger writes, never re-minted.
- Fingerprint: `include_blackboard` affects the built brief *content*, which
  already participates in fingerprinting as `Input` - acceptable and
  correct (different brief = different work). No handle/state fields on
  `Task`. Pin test from `01` still passes.
- No privileged tools leak to children: `post_message` is spawned-scope
  only; `run_messages` is session-scope only.
- One result per task (INV-AG-21): a task that dies while parked still
  emits its envelope with an existing fixed-vocabulary reason; the new
  `awaiting_input` status must not collide with the terminal `blocked`
  that invariant already uses.

## 5. Verification

- Unit: quota and budget enforcement; CAS transitions
  `running ↔ awaiting_input` incl. races with cancel (first
  return-to-running transition in the codebase); timeout path
  returns `no_answer` and resumes.
- Integration (style of `internal/cli/delegation_test.go` and
  `dispatch_timeout_test.go`): finding round-trip child→ledger→result
  envelope→`run_messages`; question+user-answer; question+timeout;
  cancel-while-parked salvage.
- `verify-code-change` doctrine after implementation.

## 6. Open decisions

1. **Envelope field name and placement** for appended findings on
   `dispatchTaskResult` - and whether synopses count against
   `batch_result_budget_bytes` (position: yes, same pot; a message must
   never be the thing that blows the parent's budget invisibly).
2. **Blocking-in-handler occupies a pool worker.** Alternative - park by
   returning from the handler and re-entering later - requires suspending
   and resuming a mid-flight `agent.Loop`, which the codebase has no
   machinery for. Position: accept the occupied slot. This is worker
   **starvation**, not classic deadlock: `Pool.execute` runs
   `min(Workers, len(tasks))` workers, every parked child pins one for up
   to `wait_seconds`, and with small `max_workers` ready tasks starve
   behind parked ones. Mitigations: `Workers: 0` (today's default) means
   unbounded goroutines; when workers are bounded, config validation must
   require `max_pending_questions < max_workers`; and `04` must never let
   a pool-hosted agent block on a peer answer whose satisfaction needs a
   worker slot, or parked workers form a cycle. Revisit park-and-resume
   only with evidence.
3. **Should `finding` also fire a hook** (plan 45 territory)? Position:
   defer to 45; emit the lifecycle event, let 45 decide hookability.
4. Whether sibling blackboard digests need their own byte budget separate
   from the brief. Position: yes, small fixed default (2048).
