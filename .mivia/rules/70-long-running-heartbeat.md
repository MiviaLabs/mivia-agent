# Long-Running Tasks & Heartbeat Protocol

Product: **mivia**. Subagents and tool invocations can run for hours.

## Vision

Mivia is an **orchestrator**, not a quick-query tool. The harness agent (you, reading this) dispatches work that may take seconds, minutes, or **hours**. The system must support arbitrarily long tasks without hard timeout ceilings, and provide **heartbeat/progress visibility** so the orchestrator can:

- See what sub-tasks are doing at any moment
- Detect stalled tasks and cancel/retry them
- Extend deadlines reactively when progress is happening
- Report progress to the user (TUI progress bars, status lines)

## Rules

### 1. No hard timeout ceilings

Timeouts are **advisory defaults**, not enforced hard caps. Every layer must allow:

- **Zero = no timeout** (run until done)
- **Override per-invocation** via `timeout_seconds` parameter
- **Handler self-manages** - the handler (multi_step, oneshot, skill) decides its own timeout strategy

The pool's `executeOne()` MUST NOT wrap context with a timeout that overrides the handler's own ability to set a longer one. The pool's job is to **pass through** the timeout from the task config to the handler, not to enforce a ceiling.

### 2. Heartbeat events

Every long-running invocation (multi_step subagent, long tool call) SHOULD emit periodic heartbeat/progress events through the runtime `Dispatcher.Sink`.

Event types:
| Event Type | When | Payload |
|-----------|------|---------|
| `task_start` | Task begins | task ID, handler name, input summary |
| `task_heartbeat` | Every N seconds or K steps | task ID, step count, current status, elapsed time, estimated remaining |
| `task_tool_call` | Each tool invocation | task ID, tool name, args summary, duration |
| `task_progress` | Milestone reached | task ID, percent complete, description |
| `task_end` | Task completes | task ID, output summary, total duration, success/fail |
| `task_stalled` | No progress in M seconds | task ID, last activity, suggested action |

> **Implemented kinds.** The table above is the intended generic protocol; the
> code today implements analogues rather than these exact names:
> `subagent_start` / `subagent_heartbeat` / `subagent_done`
> (`internal/agent/event.go`), the `workflow_step_started` /
> `workflow_step_heartbeat` / `workflow_step_completed` kinds and
> `invocation_started` / `invocation_completed` / `invocation_retrying`
> (`internal/events/event.go`). `task_stalled` has no event kind: stalled
> detection is TUI-display-only today (`stallQuiet = 15s` in
> `internal/cli/tui_layout.go` sets the footer warning, nothing is emitted).

### 3. Orchestrator can react

The dispatcher `Sink` function receives events and can:
- Forward to TUI (progress bars, status lines)
- Forward to the orchestrator agent as observations
- Trigger automatic actions (extend deadline, cancel stalled, spawn fallback)

The orchestrator agent sees events as tool outputs or delegate results - it can make decisions based on them.

### 4. Context cancellation is always respected

Even without timeouts, context cancellation must work:
- User presses Ctrl-C → parent context cancels → all children cancel
- Orchestrator detects stall → cancels specific task context
- Task respects `ctx.Done()` and exits promptly

### 5. Budgets are the real bound

Timeouts are advisory; **budgets** (token, step, tool-call) are the real enforceable limits:
- `MaxSteps` bounds LLM turns in multi_step
- `MaxTokens` bounds per-response length
- `Budget` bounds total resource consumption
- These prevent infinite loops without hard timeouts

## Implementation Guidance

## Implementation Status

All rules are implemented as of 2025-07-17:

| Rule | Status | File(s) |
|------|--------|---------|
| No hard timeout ceilings | ✅ | `internal/config/defaults.go`, `internal/subagents/subagents.go` |
| Pool passes through advisory timeouts | ✅ | `internal/subagents/subagents.go:executeOne()` |
| Handler timeout layering (tighter than parent only) | ✅ | `internal/subagents/multi_step.go:timeoutContext()` |
| Heartbeat events emitted every 30s | ✅ | `internal/subagents/multi_step.go:emitHeartbeat()` |
| Heartbeat visible in TUI status bar | ✅ | `internal/cli/brand.go:renderWorkChrome()` |
| Heartbeat visible in composer bottom bar | ✅ | `internal/cli/composer.go:composerBottomBorder()` |
| Event-bus heartbeat propagation | ✅ | `internal/subagents/multi_step.go:emitHeartbeat()` (30s) → `internal/cli/dispatcher_handlers.go:OnEventForMultiStep()` → `internal/cli/subagent_progress.go:emitSubagentProgress()` → `events.Bus` (interactive TUI only); workflow steps also emit `controller.ProgressSink` events |
| Stalled detection (15s quiet since last activity; TUI footer warning only) | ✅ | `internal/cli/tui_layout.go:104` (`stallQuiet = 15 * time.Second`) |
| Enriched results (elapsed, steps, step_count) | ✅ | `internal/subagents/multi_step.go`, `internal/cli/dispatch.go` |
| timeout_seconds override parameter | ✅ | `internal/cli/delegate.go`, `internal/cli/dispatch.go` |
| Agent prompt awareness | ✅ | `internal/cli/prompt.go`, `.mivia/agents/*.toml` |

**Workflow step heartbeat cadence.** Three clocks keep a running workflow step
observable:

- **Claim refresh 100s** = `workflowledger.DefaultClaimLease` (5m) / 3 → `internal/workflows/controller/linear_claim_heartbeat.go`
- **Subagent heartbeat 30s** → `internal/subagents/multi_step.go:emitHeartbeat()` (fixed 30s ticker)
- **Agent-step join heartbeat** — while an `agent` step's coordinator join is
  live, the join watchdog emits `ProgressStepHeartbeat` (kind `step_heartbeat`,
  surfaced as `workflow_step_heartbeat` on the events bus) once per watchdog
  tick, every `min(bound/8, 30s)` (up to 30s; a 100ms floor guards tiny test
  bounds) → `internal/workflows/controller/agent_step_join.go`
  (`joinWatchdogTickInterval` / `watchJoinLiveness`), surfaced via
  `internal/events/event.go` / `internal/cli/workflow_tool_engine.go`

**Gates are not on the heartbeat clock.** Evidence gates and human gates emit
no `step_heartbeat`: they report `gate_started` (evidence gates) or
`approval_requested` (human gates, when the run parks at `waiting_approval`)
at start and `step_completed` at completion →
`internal/workflows/controller/linear_gates.go` (evidence-gate admission,
human-gate park) and `internal/workflows/controller/linear_human.go`
(resolution).

## See Also

- `.mivia/rules/50-concurrency-subagents.md` - concurrency caps and worker pool rules
- `internal/runtime/dispatcher.go` - event sink and handler dispatch
- `internal/agent/loop.go` - agent loop with step/tool events
- `internal/events/event.go` - implemented event kinds (`subagent_*`, `workflow_step_*`, `invocation_*`)
- `internal/workflows/controller/agent_step_join.go` - join watchdog tick and the `step_heartbeat` emitter for live agent joins
- `internal/workflows/controller/progress.go` - `ProgressSink` and `ProgressStepHeartbeat` kind
- `docs/product/workflows-guide.md` (Observability) - workflow observability levels and heartbeat cadence
