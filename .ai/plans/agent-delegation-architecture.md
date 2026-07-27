# Agent Delegation Architecture

> **A performant, reusable, centralized delegation system for mivia**
> Built on Command, Strategy, and Worker Pool patterns
> Status: Core runtime and bounded Markdown skill loading implemented and tested.

---

## Status Summary

| Phase | Component | Status | Tests |
|-------|-----------|--------|-------|
| 0 | Foundation (dispatcher, config, OneShotHandler) | ✅ Done | 11 tests |
| 1 | `delegate` tool (single subagent) | ✅ Done | 4 tests |
| 2 | `dispatch_tasks` tool (parallel subagents) | ✅ Done | 4 tests |
| 3 | Multi-step subagents (full agent loop, all tools) | ✅ Done | 6 tests |
| 4 | Skills integration (bounded Markdown + registry surfaces) | ✅ Done | 3 tests |
| 5 | TUI events (subagent lifecycle observability) | ✅ Done | 2 tests |

---

## 1. Architecture

### 1.1 Execution Flow

```
Model → tool call → agent.Loop → dispatcher.Invoke(Kind: Tool)
  │
  ├── delegate(task) → OneShotHandler → 1 LLM call, no tools
  │
  ├── dispatch_tasks(tasks[]) → Pool → OneShotHandler × N
  │
  └── delegate(task, multi_step=true) → MultiStepHandler
       → agent.Loop (restricted tools, step cap)
       → read_file, grep, write_file, run_command, etc.
       → Structured JSON result
```

### 1.2 Tool Access by Delegation Type

| Delegation Type | Handler | Tool Access | Use Case |
|----------------|---------|-------------|----------|
| `delegate` (one-shot) | `OneShotHandler` | None (1 LLM call) | Summarization, classification, analysis |
| `dispatch_tasks` (one-shot) | `OneShotHandler` × N | None (N LLM calls) | Parallel independent research |
| `delegate` (multi-step) | `MultiStepHandler` | All tools except delegate/dispatch_tasks | Complex multi-step work needing tools |

### 1.3 Multi-Step Handler Design

```
MultiStepHandler.Invoke()
  ├── Strip recursion tools: remove "delegate", "dispatch_tasks" from registry
  ├── Create mini agent.Loop with restricted registry
  ├── Inject sub-agent system prompt
  ├── Run with MaxSteps cap (default 8)
  ├── Enforce per-step timeout (default 60s)
  └── Return structured JSON result
```

Key safety properties:
1. **No recursion**: `delegate` and `dispatch_tasks` tools are removed from subagent's tool registry
2. **Step cap**: Hard limit on LLM turns (default 8, configurable)
3. **Timeout**: Per-call and total timeout enforcement
4. **Context propagation**: Parent cancel = subagent cancel
5. **Depth limit**: Dispatcher enforces max nesting depth

---

## 2. Implementation

### Phase 0: Foundation
- Config types for subagents (TOML + defaults)
- `runtime.Dispatcher` wiring through Session
- `OneShotHandler` for single LLM calls
- Factory function `NewSessionDispatcher()`

### Phase 1: `delegate` Tool
- Single-task delegation via Pool
- One-shot LLM call (no tools)
- JSON schema validation

### Phase 2: `dispatch_tasks` Tool
- Multi-task parallel delegation
- Dependency ordering (DAG) via Pool
- Per-task status reporting
- Partial-failure mode support

### Phase 3: Multi-Step Subagents

**Handler**: `MultiStepHandler` in `internal/subagents/multi_step.go`
- Receives completer, tools registry, and config
- Strips `delegate` and `dispatch_tasks` from registry
- Creates mini `agent.Loop` with step cap and timeout
- Returns `{"output": "...", "status": "completed", "steps": N}`

**Tool integration**: Extended `delegate` tool accepts `multi_step` parameter.
When true, routes to MultiStepHandler instead of OneShotHandler.

### Phase 4: Skills Integration

**Wiring**: `skills.Registry.RegisterAllAsSubagents(d)` registers each skill as
`Subagent` kind in the dispatcher. Skills become callable by name from
the Pool when a registry is supplied to `NewSessionDispatcher` (e.g.
`Task{Name: "analyze_code"}` uses the skill handler). Normal CLI startup now
loads bounded `.ai/skills/*/SKILL.md` instruction documents into this registry.
The loader passes Markdown to the completer as a system instruction and never
executes embedded code.

### Phase 5: TUI Events

**Events**: `EventDelegateStart`, `EventDelegateEnd`, `EventParallel`
Track subagent lifecycle in the agent loop event system.

**Rendering**: TUI shows grouped subagent panel with per-task status.

---

## 3. Tests

| Test Suite | Tests | What It Covers |
|-----------|-------|----------------|
| `OneShotHandler` (unit) | 6 | Invoke, empty, cancel, timeout, invalid input |
| `Pool` (unit) | 3 | DAG ordering, cycles, partial failures |
| `delegate` tool | 4 | Valid, empty, missing task, cancel |
| `dispatch_tasks` tool | 4 | Valid, empty, dependencies, cancel |
| `MultiStepHandler` | 6 | Invoke, tool access, result, recursion blocked, step cap, cancel |
| `Skills as Subagents` | 2 | Registration, routing |
| `TUI Events` | 2 | Event emission, bridge rendering |
| `Session wiring` | 1 | Delegation tools registered |
| `Config` | 2 | Defaults, TOML parsing |

All tests pass with `-race`.

---

## 4. Design Decisions

### D1: Full Tool Access (Not Read-Only)

**Decision**: Multi-step subagents get ALL tools except delegate/dispatch_tasks.

**Rationale**: The user requires subagents to perform real work (read, write, edit,
run commands). Read-only restriction would make multi-step subagents useless for
actual coding tasks. The recursion prevention (removing delegate/dispatch_tasks)
is sufficient for safety.

### D2: Single `delegate` Tool with `multi_step` Flag

**Decision**: One tool for both one-shot and multi-step, distinguished by a parameter.

**Rationale**: The model already knows `delegate`. Adding a `multi_step` boolean
is simpler than introducing a third tool name. The handler selection happens in
Execute() based on the flag.

### D3: Skills as Subagent Handlers

**Decision**: Skills are registered as both `Skill` and `Subagent` kinds.

**Rationale**: This lets the Pool invoke skills by name without needing a separate
skill invocation path. The Pool already routes through the dispatcher with
`Kind: Subagent`.

### D4: Event Kinds Are Extensible

**Decision**: New event kinds (`EventDelegateStart`, `EventDelegateEnd`)
extend the existing `agent.EventKind` enum alongside `EventToolStart`, etc.

**Rationale**: Consistent with existing event system. The TUI bridge already
handles the existing event types; adding new types is a small switch case addition.

---

## 5. File Manifest

| File | Phase | Purpose |
|------|-------|---------|
| `internal/subagents/oneshot.go` | 0 | OneShotHandler implementation |
| `internal/subagents/oneshot_test.go` | 0 | OneShotHandler tests |
| `internal/config/types.go` | 0 | SubagentConfig struct |
| `internal/config/defaults.go` | 0 | Default config values |
| `internal/config/load.go` | 0 | Config resolution |
| `internal/chat/session.go` | 0 | Dispatcher field + wiring |
| `internal/cli/dispatcher.go` | 0/1/2 | NewSessionDispatcher factory |
| `internal/cli/chat_repl.go` | 0 | CLI wiring |
| `internal/cli/delegate.go` | 1/3 | delegate + multi_step tool |
| `internal/cli/dispatch.go` | 2 | dispatch_tasks tool |
| `internal/cli/delegation_test.go` | 1/2/3 | Delegation tool tests |
| `internal/subagents/multi_step.go` | 3 | MultiStepHandler |
| `internal/subagents/multi_step_test.go` | 3 | MultiStepHandler tests |
| `internal/cli/prompt.go` | 1 | Prompt additions |
| `internal/skills/skills.go` | 4 | RegisterAllAsSubagents |
| `internal/agent/loop.go` | 5 | Event kinds for delegation |
| `internal/cli/tui_events.go` | 5 | Event rendering |
| `internal/cli/tui_stream.go` | 5 | Bridge tool event type |
