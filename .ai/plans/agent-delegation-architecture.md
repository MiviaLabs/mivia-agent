# Agent Delegation Architecture

> **A performant, reusable, centralized delegation system for mivia**
> Built on Command, Strategy, and Worker Pool patterns
> Status: Design · Review: Required before implementation

---

## 0. Why a New Plan

The previous approach ("subagents") was built from the engine outward — scaffold the Pool first, then figure out how to wire it. That created orphaned code (`subagents.Pool` exists but nothing calls it). This plan inverts the approach: **design from the model's perspective outward**.

The question isn't "how does the Pool work?" — it's "what does the model need, and what's the cleanest bridge?"

---

## 1. Research & Pattern Analysis

### Design Patterns Identified in the Existing Codebase

| Pattern | Where It Is | Status |
|---------|-------------|--------|
| **Command** | `runtime.Request` (command) → `runtime.Handler` (handler) → `runtime.Dispatcher` (invoker) | ✅ Present |
| **Worker Pool** | `subagents.Pool` with bounded goroutines + job channel | ✅ Present |
| **Strategy** | Different `Kind` (Tool, Skill, Subagent) dispatch to different handlers | ✅ Present |
| **Registry** | `tools.Registry` and `runtime.Dispatcher.handlers` | ✅ Present |
| **Capability** | `tools.Capability` for scheduling/safety metadata | ✅ Present |
| **Scheduler** | `toolScheduler` with per-resource-key locking | ✅ Present |

### What's Missing: The Bridge Patterns

| Pattern | What It Solves | Status |
|---------|----------------|--------|
| **Adapter** | Model tool calls → Pool task scheduling | ❌ Missing |
| **Composite** | Tool wraps Pool + Dispatcher as a single capability | ❌ Missing |
| **Facade** | Unified `SessionDispatcher` hides handler registration complexity | ✅ Partial |
| **Chain of Responsibility** | Dispatcher routes by Kind, falls through if no handler | ❌ Missing |

### Industry Research Synthesis

From OpenAI Agents SDK, Microsoft orchestrator-subagent, LangGraph subgraphs, and Google Cloud agent patterns:

| Principle | How We Apply It |
|-----------|-----------------|
| **Agent-as-Tool** | Delegation is a tool the model calls — not a separate protocol | `delegate` tool |
| **Bounded fan-out** | Subagents share the parent's rate limits and budgets | Pool policy from parent config |
| **Structured results** | Subagents return typed JSON, not free text | `{"output": "...", "task": "..."}` |
| **Depth limits** | Max 2 levels of nesting in practice | `Policy.MaxDepth = 3` |
| **Cancellation trees** | Parent cancel = all children cancel | Context propagation |
| **One-shot first** | 1 LLM call per subagent; multi-step deferred | Phase 1 only |

### Key Insight: The Pool Is a Command Scheduler

The existing `subagents.Pool` is misnamed. It's a **DAG-aware Command Scheduler**:

```
Tasks (with deps) → validate → topological sort → bounded workers → execute via Dispatcher
```

Each task is a `runtime.Request` executed via `Dispatcher.Invoke()`. The Pool handles:
- Dependency resolution (DAG)
- Worker pool management
- Context cancellation propagation
- Result aggregation

This is the right abstraction. The problem is nothing feeds it tasks.

---

## 2. Architecture

### 2.1 Layered Design

```
┌─────────────────────────────────────────────────────┐
│                    Model (LLM)                       │
│  Sees: delegate(task), dispatch_tasks(tasks[])       │
└────────────────────┬────────────────────────────────┘
                     │ tool_calls
                     ▼
┌─────────────────────────────────────────────────────┐
│  agent.Loop (existing)                               │
│  For each tool call: dispatcher.Invoke(Kind: Tool)   │
└────────────────────┬────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────┐
│  runtime.Dispatcher (Command Invoker)                │
│  Routes by Kind → Handler                            │
│  kinds: Tool, Skill, Subagent                         │
└─────────┬───────────┬───────────┬────────────────────┘
          │           │           │
          ▼           ▼           ▼
   ┌──────────┐ ┌──────────┐ ┌──────────────────┐
   │ Tool     │ │ Skill    │ │ Subagent         │
   │ handlers │ │ handlers │ │ handlers         │
   └──────────┘ └──────────┘ └──────────────────┘
                                 │
                    ┌────────────┴────────────┐
                    ▼                         ▼
           ┌──────────────────┐     ┌──────────────────┐
           │ OneShotHandler   │     │ Pool + Dispatcher│
           │ (1 LLM call)     │     │ (for fan-out)    │
           └──────────────────┘     └──────────────────┘
```

### 2.2 Execution Flow: Single Delegate

```
Model calls: delegate(task="analyze auth module")

1. agent.Loop.executeToolTask()
   → dispatcher.Invoke(Kind: Tool, Name: "delegate", Input: {task: "analyze auth module"})

2. delegateTool.Execute()
   → Creates subagents.Pool(dispatcher, policy)
   → pool.Run(ctx, [{ID: "t1", Name: "delegate", Input: "<prompt>"}])
   → Pool.executeOne() → dispatcher.Invoke(Kind: Subagent, Name: "delegate", Input: "analyze auth module")

3. OneShotHandler.Invoke()
   → 1 LLM call (no tools)
   → Returns {"output": "Auth module uses JWT...", "task": "analyze auth module"}

4. Pool returns [Result{TaskID: "t1", Output: {...}}]
   → delegateTool returns JSON string of result
   → agent.Loop sees tool result, continues
```

**Total API calls**: 1 (the subagent LLM call) + 0 (parent already paid for its own turn)

### 2.3 Execution Flow: Parallel Dispatch

```
Model calls: dispatch_tasks(tasks=[
  {id: "research_api", prompt: "Find all API endpoints in auth module"},
  {id: "research_tests", prompt: "Summarize auth test coverage"},
  {id: "summary", prompt: "Merge findings", depends_on: ["research_api", "research_tests"]}
])

1. Same dispatcher flow → dispatchTasksTool.Execute()

2. Pool validates tasks (fanout=3, depth=1)
3. Pool.execute() — phase 1: research_api and research_tests run in parallel
   Worker 1 → OneShotHandler("Find all API endpoints...") → 1 LLM call
   Worker 2 → OneShotHandler("Summarize auth test...") → 1 LLM call

4. Both complete → Pool.execute() — phase 2: summary runs
   Worker 1 → OneShotHandler("Merge findings...") → 1 LLM call (with prev results in context)

5. Pool returns ordered results → tool returns JSON
```

**Total API calls**: 3 (one per subagent task)
**Wall time**: ~max(research_api, research_tests) + summary (much faster than sequential)

### 2.4 How Tools Get the Dispatcher

The critical wiring: tools need access to the same `runtime.Dispatcher` that routed to them.

```go
// delegateTool holds a reference to the dispatcher and config
type delegateTool struct {
    dispatcher *runtime.Dispatcher
    cfg        config.SubagentConfig
}

func (t *delegateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    // Parse task from args
    // Create Pool with same dispatcher (Tool → Subagent: different Kind, no cycle)
    pool := subagents.New(t.dispatcher, subagents.Policy{...})
    results, err := pool.Run(ctx, tasks)
    // Serialize and return
}
```

The dispatcher reference is injected at construction time (see `internal/cli/dispatcher.go`).

---

## 3. Design Patterns Applied

### Command Pattern

```
Command:     runtime.Request{Kind, Name, Input, Timeout, ...}
Receiver:    runtime.Handler (OneShotHandler, toolHandler, skillHandler)
Invoker:     runtime.Dispatcher.Invoke()
Client:      agent.Loop (for Tool) or subagents.Pool (for Subagent)
```

Already present. The key insight is that the **same interface** is used for all execution levels — tools, skills, and subagents. This means any handler can be invoked through the same dispatcher.

### Strategy Pattern (for delegation modes)

```
Context:     delegateTool / dispatchTasksTool
Strategy:    How tasks are executed
             - Single: Pool with 1 task (delegate)
             - Fan-out: Pool with N independent tasks (dispatch_tasks)
             - Pipeline: Pool with N dependent tasks (dispatch_tasks with depends_on)
```

The Pool handles all strategies. The tools just configure the task list differently.

### Adapter Pattern

```
Target:      runtime.Dispatcher.Invoke(Kind: Subagent, ...)
Adaptee:     tools.Tool.Execute(ctx, args)
Adapter:     delegateTool / dispatchTasksTool
```

The tools adapt between the model-facing tool interface (JSON input/output) and the dispatcher's command execution interface.

### Worker Pool Pattern

```
Tasks:       Queue
Workers:     Bounded goroutines (Policy.Workers)
Result:      Aggregated ordered results
```

Already present in `subagents.Pool.execute()`.

### Composite Pattern

```
Component:   runtime.Handler (individual handler)
Composite:   subagents.Pool (executes multiple handlers with coordination)
Leaf:        OneShotHandler, toolHandler
```

The Pool is a composite handler — it takes multiple tasks and executes them through the dispatcher.

---

## 4. Implementation Plan

### Phase 1: `delegate` Tool (Must ship)

The model calls `delegate(task="analyze auth module")` and gets a one-shot LLM response.

**Files:**
- `internal/tools/delegate.go` — New
- `internal/tools/tools.go` — Register tool
- `internal/cli/dispatcher.go` — Pass dispatcher to tool
- `internal/cli/prompt.go` — Add prompt description

**Size:** ~100 lines of Go
**Risk:** Low — no new abstractions, just connect existing pieces

**Key detail:** The tool registers with the *tool registry*, not the dispatcher. The dispatcher is passed to the tool at construction time:

```go
// In NewDefaultRegistry (modified):
func NewDefaultRegistry(opts DefaultOptions) *Registry {
    reg := NewRegistry()
    // ... existing tools ...
    if opts.Dispatcher != nil && opts.SubagentConfig != nil {
        reg.Register(&delegateTool{
            dispatcher: opts.Dispatcher,
            cfg:        *opts.SubagentConfig,
        })
    }
    return reg
}
```

Wait — this creates a circular dependency: `NewDefaultRegistry` needs a dispatcher, but the dispatcher is created from the registry via `NewToolDispatcher`.

**Solution:** Add the delegate tool *after* dispatcher creation, by extending `NewSessionDispatcher`:

```go
func NewSessionDispatcher(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig) *runtime.Dispatcher {
    // 1. Create dispatcher with tool handlers
    d := runtime.NewToolDispatcher(reg, runtime.Policy{...})

    // 2. Register subagent handlers
    d.Register(runtime.Subagent, "delegate", &OneShotHandler{...})

    // 3. Add delegate tool to registry (now we have dispatcher reference)
    reg.Register(&delegateTool{dispatcher: d, cfg: cfg})

    return d
}
```

**To make this work, `NewDefaultRegistry` needs to support optional post-creation tool injection.** OR we add tools to the registry after `NewDefaultRegistry` returns.

The cleanest approach: `NewDefaultRegistry` doesn't know about delegate. The tool is added after:

```go
reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
d := NewSessionDispatcher(reg, comp, model, cfg)
// d already registered delegate tool in reg
```

### Phase 2: `dispatch_tasks` Tool (Extends Phase 1)

The model calls `dispatch_tasks(tasks=[{id, prompt, depends_on}])` and gets parallel execution.

**Files:**
- `internal/tools/dispatch.go` — New
- `internal/tools/tools.go` — Register tool

**Size:** ~80 lines of Go
**Risk:** Low — reuses Pool, just wraps with multi-task schema

### Phase 3: Skills Integration (Optional Enhancement)

Wire `internal/skills` as Subagent handlers.

**Files:**
- `internal/skills/skills.go` — `RegisterAllAsSubagents()`
- `internal/cli/dispatcher.go` — Call it

### Phase 4: TUI + Events (Observability)

Emit events for subagent lifecycle. Show parallel progress.

### Phase 5: Multi-Step Subagents (Future)

Subagent with tool access. Requires read-only tool restriction, file locking.

---

## 5. Self-Critique & Validation

### Challenge 1: "The Pool and the Dispatcher overlap in responsibility"

**Claim:** Both `subagents.Pool` and `runtime.Dispatcher` manage concurrency and routing. This is duplication.

**Validation:** They serve different purposes:
- **Dispatcher**: Routes a *single request* to the right handler by Kind/Name. It's the Command Invoker.
- **Pool**: Schedules *multiple requests* with dependency ordering. It's the DAG Scheduler.

The Pool uses the Dispatcher for execution — it's not overlapping, it's composing. The Pool is a *client* of the Dispatcher.

### Challenge 2: "The delegate tool creates a new Pool per invocation"

**Claim:** This is wasteful. Cache the Pool.

**Analysis:** A Pool is a lightweight struct (dispatcher pointer + policy). Creating one per call is O(1) and avoids stale state. The goroutines are created per `Run()` call, which is necessary anyway. Reusing a Pool across calls would require resetting state — more complex, no benefit.

**Decision:** New Pool per invocation. Accept.

### Challenge 3: "Parallel subagents burn through API quota"

**Claim:** 4 parallel subagents = 4 concurrent LLM calls. This is expensive.

**Analysis:** The existing agent.Loop already parallelizes tool calls (up to `MaxConcurrentTools`, default 4). Parallel subagents are the same model. The difference is each subagent makes an *additional* LLM call on top of the parent's call. With 4 subagents at 1 call each = 4 extra API calls per user turn.

**Mitigation:**
- Default fan-out is conservative (max 4 workers, 16 total tasks)
- Configurable via TOML
- Subagent prompt says "be concise" to limit token usage
- The `PartialResults` mode allows partial success
- Per-task timeout prevents runaway costs

**Decision:** Acceptable for Phase 1. Monitor and add budget caps in Phase 6.

### Challenge 4: "One-shot is too limited — what about tool-using subagents?"

**Claim:** A subagent that can't use tools is barely useful.

**Analysis:** One-shot is useful for: classification, summarization, comparison, structured extraction, code review (given the code as context). The parent model gathers context via tools, then delegates analysis to subagents. This is the "gather then analyze" pattern.

Multi-step (tool-using) subagents are Phase 5. They require:
- Read-only tool registry construction
- File-path conflict detection
- Per-step context management
- Cancellation at any step

**Decision:** One-shot first. Validate with real usage. Multi-step when the one-shot ceiling is reached.

### Challenge 5: "The system has too many abstractions for what it does"

**Claim:** Tool → Dispatcher → Pool → Dispatcher → Handler is too many layers.

**Analysis:**
- Tool → Dispatcher: 1 hop (existing, required for all tool calls)
- Dispatcher → Tool handler: 1 hop (existing)
- Tool → Pool: 1 hop (new)
- Pool → Dispatcher: 1 hop (new, different Kind)
- Dispatcher → Subagent handler: 1 hop (new)

5 hops for the simplest case. Is this too many?

**Comparison with alternatives:**
- **Direct handler call**: Tool calls OneShotHandler directly. Saves 2 hops (Pool + Dispatcher). But loses dependency ordering, worker pooling, cancellation, idempotency, depth tracking, budget enforcement. The Pool provides all of these.
- **No tool, direct model access**: Skip the tool layer, let the model call Subagent directly. But the model can only call tools — that's the OpenAI protocol. Subagent is not a tool kind, it's a dispatcher kind.

**Decision:** 5 hops is acceptable for the guarantees the Pool provides. Each hop is an in-process function call (microseconds). The LLM call dominates latency (seconds).

### Challenge 6: "Why not use errgroup instead of a custom Pool?"

**Claim:** Go's `errgroup` with `SetLimit` already provides bounded concurrency.

**Analysis:** errgroup is sufficient for simple fan-out. It does NOT provide:
- Dependency ordering (DAG execution)
- Partial-failure mode (skip failed deps, continue)
- Idempotency detection
- Deterministic result ordering
- Budget tracking
- Depth limit enforcement

The Pool provides all of these. errgroup is used *inside* the Pool for the worker goroutines (the parallel execution phase), but the orchestration logic around it is non-trivial.

**Decision:** Keep the Pool. It's not over-engineering — it's encoding real requirements that errgroup doesn't meet.

### Challenge 7: "Should delegate and dispatch_tasks be one tool or two?"

**Claim:** Having two tools is confusing. One tool with optional arrays is simpler.

**Analysis:**

| Approach | Pros | Cons |
|----------|------|------|
| **Two tools** | Clear semantics; model chooses; simpler schema per tool | More tools in the model's context |
| **One tool** | Fewer tools; model uses same call pattern | Complex schema ("either single task or array"); model might misuse |

**Decision:** Two tools. The model benefits from clear semantics. Each tool has a focused purpose.

---

## 6. File Manifest

### Phase 1: `delegate` Tool

| File | Action | Purpose |
|------|--------|---------|
| `internal/tools/delegate.go` | **Create** | `delegateTool` implementation |
| `internal/tools/tools.go` | **Modify** | Add `Dispatcher` and `SubagentConfig` fields to `DefaultOptions`; register delegate tool |
| `internal/cli/dispatcher.go` | **Modify** | Call `reg.Register(&delegateTool{...})` after dispatcher creation |
| `internal/cli/prompt.go` | **Modify** | Add `delegate` description to agent prompt |
| `internal/tools/delegate_test.go` | **Create** | Tests for delegate tool |

### Phase 2: `dispatch_tasks` Tool

| File | Action | Purpose |
|------|--------|---------|
| `internal/tools/dispatch.go` | **Create** | `dispatchTasksTool` implementation |
| `internal/tools/tools.go` | **Modify** | Register dispatch_tasks tool |
| `internal/cli/dispatcher.go` | **Modify** | Register dispatch_tasks tool |
| `internal/tools/dispatch_test.go` | **Create** | Tests for dispatch_tasks tool |

### Phase 3 (Future): Skills Wiring

| File | Action | Purpose |
|------|--------|---------|
| `internal/skills/skills.go` | **Modify** | Add `RegisterAllAsSubagents()` |
| `internal/cli/dispatcher.go` | **Modify** | Wire skills registry |

### Phase 4 (Future): TUI Events

| File | Action | Purpose |
|------|--------|---------|
| `internal/agent/loop.go` | **Modify** | Add subagent event kinds |
| `internal/cli/toolui.go` | **Modify** | Render subagent progress |

---

## 7. Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| **Cost**: N subagents = N extra LLM calls | Medium | Medium | Default 4 workers; configurable; per-task timeout |
| **Latency**: Subagents sequentialize at parent | Low | Medium | Pool runs independent tasks in parallel; parent is blocked only by tool call |
| **Depth**: Model chains delegates recursively | Low | High | Hard cap at 3; enforced by dispatcher policy |
| **Quality**: Subagent without tools hallucinates | Medium | Medium | Parent provides context in task prompt; prompt says "do not invent" |
| **Wiring**: Tool needs dispatcher, needs tools = cycle | High | Low | Solved: add tool to registry after dispatcher creation |
| **Observability**: User can't see parallel progress | Medium | Low | Phase 4 adds TUI rendering; Phase 1 shows simple status |
| **Idempotency**: Same delegate called twice | Low | Low | Pool has idempotency keys; dispatcher caches results |

---

## 8. Implementation Order

```
Phase 1: delegate tool           ~1 day     ← START HERE
  └─ delegate.go, modify tools.go, dispatcher.go, prompt.go
  └─ Test: unit + integration + race

Phase 2: dispatch_tasks tool     ~1 day     ← NEXT
  └─ dispatch.go, modify tools.go, dispatcher.go
  └─ Test: unit + integration + race + concurrency stress

Phase 3: Skills wiring           ~0.5 day   ← OPTIONAL
  └─ RegisterAllAsSubagents
  └─ Test: skill routing

Phase 4: TUI + Events            ~1 day     ← POLISH
  └─ Event kinds, rendering
  └─ Test: TUI rendering

Phase 5: Multi-step subagents    ~2-3 days  ← FUTURE
  └─ Mini agent loop, read-only scope
  └─ Test: tool isolation, cancellation
```

Each phase produces a working increment. No phase depends on a later phase.

---

## Appendix A: Pattern Reference

| Pattern | Where | Why |
|---------|-------|-----|
| **Command** | `runtime.Request` + `runtime.Handler` + `runtime.Dispatcher` | Decouples command invocation from execution |
| **Strategy** | `subagents.Pool` with different task lists | Different delegation modes share the same engine |
| **Adapter** | `delegateTool` adapts `Tool.Execute` → `dispatcher.Invoke(Subagent)` | Bridges the tool surface to the command layer |
| **Worker Pool** | `subagents.Pool.execute()` with bounded goroutines | Controls concurrency without unbounded spawning |
| **Composite** | `subagents.Pool` as a collection of `runtime.Handler` calls | Treats single and batched execution uniformly |
| **Registry** | `tools.Registry` + `runtime.Dispatcher.handlers` | Centralized lookup for handlers by name |
| **Chain of Resp.** | `runtime.Dispatcher.Invoke()` routes by `Kind` | Separation of concerns between Tool/Skill/Subagent |
| **Facade** | `NewSessionDispatcher()` | Simplifies complex handler registration |

## Appendix B: Code Change Specifications

### delegateTool.Execute() pseudocode

```go
func (t *delegateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    var params struct {
        Task string `json:"task"`
    }
    if err := json.Unmarshal(args, &params); err != nil {
        return `{"error":"invalid arguments"}`,
            fmt.Errorf("delegate: %w", err)
    }
    if params.Task == "" {
        return `{"error":"task is required"}`,
            fmt.Errorf("delegate: task is required")
    }

    pool := subagents.New(t.dispatcher, subagents.Policy{
        Workers:  t.cfg.MaxWorkers,
        MaxDepth: t.cfg.MaxDepth,
        Timeout:  time.Duration(t.cfg.DefaultTimeout) * time.Second,
    })

    // Marshal task as a JSON string for the OneShotHandler
    input, _ := json.Marshal(params.Task)

    tasks := []subagents.Task{{
        ID:      "d1",
        Name:    "delegate",
        Owner:   "parent",
        Input:   input,
        Timeout: time.Duration(t.cfg.DefaultTimeout) * time.Second,
    }}

    results, err := pool.Run(ctx, tasks)
    if err != nil {
        return fmt.Sprintf(`{"error":"%s"}`, err), err
    }

    // Serialize first result
    if len(results) == 0 {
        return `{"status":"no_result"}`, nil
    }
    r := results[0]
    if r.Err != nil {
        return fmt.Sprintf(`{"error":"%s"}`, r.Err), r.Err
    }
    return string(r.Output), nil
}
```

### DefaultOptions changes

```go
type DefaultOptions struct {
    Workspace      *workspace.Root
    Dispatcher     *runtime.Dispatcher   // NEW
    SubagentConfig *config.SubagentConfig // NEW
    RunAllowlist   []string
    RunTimeoutSec  int
    MaxReadBytes   int
    MaxOutputBytes int
    MaxWriteKB     int
}
```

### NewSessionDispatcher changes

```go
func NewSessionDispatcher(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig) *runtime.Dispatcher {
    // Step 1: create dispatcher with tool handlers only
    d := runtime.NewToolDispatcher(reg, runtime.Policy{
        MaxDepth:  cfg.MaxDepth,
        MaxBudget: cfg.DefaultBudget,
    })

    // Step 2: register subagent handlers
    h := &subagents.OneShotHandler{
        Completer:    comp,
        Model:        model,
        SystemPrompt: cfg.SystemPrompt,
    }
    d.Register(runtime.Subagent, "delegate", h)
    d.Register(runtime.Subagent, "oneshot", h)

    // Step 3: add delegate tool to registry (now dispatcher exists)
    reg.Register(&delegateTool{dispatcher: d, cfg: cfg})
    reg.Register(&dispatchTasksTool{dispatcher: d, cfg: cfg})

    return d
}
```
