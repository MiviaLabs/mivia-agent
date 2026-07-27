# Plan: Long-Running Tasks & Heartbeat Architecture

**Date:** 2025-07-17
**Status:** In Progress — Steps 1-5 complete, Steps 6-7 pending

## Vision

Mivia is an **orchestrator** for arbitrarily long work. It must support tasks that run for seconds, minutes, or **hours** without hard timeout ceilings, and provide **heartbeat visibility** so the orchestrator agent can track, react, and adapt.

## Problem

### Timeout Architecture Today

```
dispatch_tasks(handler:"multi_step", timeout_seconds: 600)
  └─ Pool.executeOne() applies context.WithTimeout(ctx, 600s)  ← HARD CEILING
       └─ MultiStepHandler.run() tries TotalTimeout=180s      ← USELESS, parent is shorter
            └─ agent.Loop with MaxSteps=8, ToolTimeout=60s
```

**What's wrong:**
1. Pool wraps context with timeout BEFORE handler gets it — handler can't extend
2. Pool timeout is a **hard ceiling** that overrides handler's own timeout strategy
3. Default 600s (10min) is too short for hours-long orchestration
4. Zero timeout = 60s from policy fallback (not infinite)
5. No heartbeat/progress events — orchestrator sees nothing until task completes or times out
6. No way for orchestrator to react mid-task (cancel stalled, extend, redirect)

### Root Cause

The architecture was designed for quick queries (~30s-2min), not long-running orchestration. Timeouts were added as safety nets but became hard ceilings. The pool is a scheduling layer that shouldn't dictate execution timeouts.

## Changes

### 1. Pool: Stop overriding handler timeouts (`internal/subagents/subagents.go`)

**`executeOne()`** currently:
```go
timeout := t.Timeout           // from config/tool params
if timeout <= 0 {
    timeout = p.p.Timeout      // policy fallback (600s)
}
if timeout > 0 {
    taskCtx = context.WithTimeout(ctx, timeout)  // BAD: hard ceiling
}
r := p.d.Invoke(taskCtx, runtime.Request{...})
```

**Fix:**
```go
// Pass timeout to handler as advisory metadata, not context deadline.
// The handler decides whether/how to enforce it.
r := p.d.Invoke(ctx, runtime.Request{
    Timeout: t.Timeout,   // advisory — handler's choice
    ...
})
// Pool's job is scheduling and cancellation, not timing out work.
```

**Why safe:** Context cancellation still works (Ctrl-C, parent cancel, orchestrator cancel). Budgets (`MaxSteps`, `MaxTokens`) bound resource usage. The handler (multi_step) has its own `TotalTimeout` and `ToolTimeout` that it manages.

### 2. Multi_step: Fix timeout layering (`internal/subagents/multi_step.go`)

**`run()`** currently:
```go
callCtx := ctx
if h.TotalTimeout > 0 {
    callCtx = context.WithTimeout(ctx, h.TotalTimeout)  // can't extend parent
}
```

**Fix:**
```go
callCtx := ctx
if h.TotalTimeout > 0 {
    // Only apply if it's SHORTER than parent (don't try to extend)
    if parentDeadline, ok := ctx.Deadline(); !ok || h.TotalTimeout < time.Until(parentDeadline) {
        var cancel context.CancelFunc
        callCtx, cancel = context.WithTimeout(ctx, h.TotalTimeout)
        defer cancel()
    }
}
```

**Also:** Increase defaults for long-running work:
```go
// Today:
MaxSteps:     8   // ~8 LLM turns
ToolTimeout:  60s // per tool call
TotalTimeout: 180s // 3 min wall clock

// Tomorrow (or configurable):
MaxSteps:     100 // allow long chains
ToolTimeout:  300s // 5 min per tool call (some tools are slow)
TotalTimeout: 0   // no wall clock limit (or very high)
```

### 3. Heartbeat/Progress events (`internal/subagents/multi_step.go` + `internal/agent/loop.go`)

Add periodic heartbeat emission from long-running handlers:

```go
// In MultiStepHandler.run(), spawn a heartbeat goroutine:
heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
defer heartbeatCancel()

go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-heartbeatCtx.Done():
            return
        case <-ticker.C:
            if h.OnEvent != nil {
                h.OnEvent(agent.Event{
                    Type: "subagent_heartbeat",
                    Metadata: // task ID, steps, elapsed, etc.
                })
            }
        }
    }
}()
```

**Event types:**
| Event | Trigger | Purpose |
|-------|---------|---------|
| `subagent_start` | Task begins | Orchestrator knows it's running |
| `subagent_step` | Each LLM turn | Progress indicator |
| `subagent_tool_call` | Each tool invocation | What's happening right now |
| `subagent_heartbeat` | Every 30s of wall time | "Still alive" signal |
| `subagent_progress` | Milestone (e.g., "search complete") | Meaningful progress |
| `subagent_stalled` | No tool calls in 120s | Potential stall detected |
| `subagent_end` | Task completes | Final result available |

### 4. Task-level timeout_seconds override (`internal/cli/dispatch.go` + `internal/cli/delegate.go`)

✅ **Already done** in previous changes — both tools accept `timeout_seconds` parameter.

But the default should be higher or infinite:
- `dispatch_tasks` default: keep 600s but document that 0 = no timeout
- `delegate` default: same
- Allow `timeout_seconds: 0` to mean "no timeout" (pass through to handler with zero)

### 5. Config defaults: Make zero mean infinite (`internal/config/defaults.go`)

```go
// Before:
DefaultTimeout: 600,  // 10 min hard cap
// After:
DefaultTimeout: 0,    // 0 = no default timeout, handlers decide
```

This requires the pool and handlers to handle zero correctly (they already mostly do — `timeout <= 0` skips `WithTimeout`).

### 6. TUI: Show heartbeat events (`internal/cli/tui.go`)

The TUI already handles events. Add rendering for:
- `subagent_heartbeat` → subtle pulse indicator (⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏ spinner or elapsed counter)
- `subagent_step` → step counter per task
- `subagent_tool_call` → current tool name per task
- `subagent_stalled` → warning indicator

### 7. Also update the default timeout in `internal/agent/session.go` or wherever the top-level agent timeout is set

Check `internal/agent/loop.go` for the top-level agent loop's context timeout.

## Order of Execution

| Step | File(s) | Change | Risk |
|:----:|---------|--------|:----:|
| 1 | `internal/config/defaults.go` | Change `DefaultTimeout` to 0 (no default) | **Medium** — existing tests expect 600 |
| 2 | `internal/subagents/subagents.go` | Remove `context.WithTimeout` from `executeOne()`, pass `Timeout` as advisory in `Request` | **Medium** — changes pool behavior |
| 3 | `internal/subagents/multi_step.go` | Fix timeout layering (don't extend parent), add heartbeat goroutine | **Medium** — new goroutine per subagent |
| 4 | `internal/agent/loop.go` | Add heartbeat/step events, increase default MaxSteps | Low |
| 5 | `internal/cli/tui.go` | Render heartbeat/progress events | Low |
| 6 | `internal/runtime/dispatcher.go` | Add heartbeat event types if needed | Low |
| 7 | Tests | Update tests for new timeout behavior | Medium |

## Rollback Strategy

| Scenario | Rollback |
|----------|----------|
| Subagents hang forever (no timeout) | Revert step 2 (pool timeout) — add back context.WithTimeout |
| Heartbeat goroutine leaks | Revert step 3 — remove heartbeat goroutine |
| TUI too noisy | Revert step 5 — filter heartbeat events from display |

## Open Questions

1. Should heartbeat interval be configurable per-task or global?
2. Should the orchestrator be able to dynamically extend a task's timeout via a tool call?
3. Should stalled detection be automatic (no tool calls in N seconds) or event-driven?
4. How do heartbeats interact with the TUI's existing spinner/status system?

## See Also

- `.ai/rules/70-long-running-heartbeat.md` — operating rules
- `.ai/rules/50-concurrency-subagents.md` — concurrency model
- `internal/runtime/dispatcher.go` — event sink
- `internal/agent/loop.go` — agent loop events

## Completed

### Step 1: DefaultTimeout changed to 0
**File:** `internal/config/defaults.go`
**Change:** `DefaultTimeout: 0` (was 600)
**Effect:** No default timeout — if no `timeout_seconds` is passed, handlers get 0 which means no timeout, run until done.

### Step 2: Pool no longer enforces hard ceiling
**File:** `internal/subagents/subagents.go`
**Change:** Removed `context.WithTimeout` wrapping from `executeOne()`. Now passes `Timeout` as advisory via `runtime.Request.Timeout` — the handler decides whether to enforce it.
**Effect:** Pool's job is scheduling and cancellation, not timing out work.

### Step 3: Multi-step and oneshot timeout layering fixed
**Files:** `internal/subagents/multi_step.go`, `internal/subagents/oneshot.go`
**Change:** Both handlers now only apply timeout if it's **tighter than parent deadline**. If parent has a shorter deadline, handler respects it. Never tries to extend beyond parent.
**Effect:** Context hierarchy is consistent — parent controls the outer bound, children can only tighten it.

### Step 4: Heartbeat event emission from multi-step subagents
**Files:** `internal/agent/loop.go`, `internal/subagents/multi_step.go`, `internal/cli/tui_events.go`
**Change:** Added `EventSubagentHeartbeat` event type. MultiStepHandler spawns a heartbeat goroutine that emits elapsed time + step count every 30 seconds. Wraps OnEvent to count steps. TUI bridge forwards heartbeats as step/status updates.
**Effect:** Orchestrator and user can see that a subagent is still making progress during long operations. Stalled tasks become visible.

### Step 5: TUI heartbeat visibility + stalled detection + MaxSteps increase
**Files:** `internal/cli/tui.go`, `internal/cli/tui_message.go`, `internal/cli/composer.go`, `internal/cli/tui_view.go`, `internal/config/defaults.go`, `internal/subagents/multi_step.go`
**Change:** TUI now captures `stepDetail` from bridge and shows it in composer bottom bar (e.g. "elapsed=1m30s steps=5"). Stalled detection: if no heartbeat for 120s, shows "⚠ stalled" in red. Default MaxSteps raised from 8→100, ToolTimeout from 60s→300s, NestedSteps from 8→100.
**Effect:** Long-running tasks have visible progress. Stalled tasks are visually flagged. Agents can sustain long chains of reasoning.
