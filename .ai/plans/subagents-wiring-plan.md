# Subagents Wiring Implementation Plan

> **Status**: Revised after critical analysis · **Review**: Required before implementation
> **Scope**: Wire the existing `internal/subagents.Pool` into the runtime, add model-facing tools, configure safety boundaries, and enable nested-agent delegation

---

## 0. Critical Analysis & Key Decisions

### 0.1 What Is a Subagent, Really?

The codebase has two competing concepts:

| Concept | What it is | Where |
|---------|-----------|-------|
| **Pool subagent** | A function handler executed with dependency ordering, resource caps, and context isolation | `internal/subagents.Pool` -> `runtime.Dispatcher` -> `runtime.Handler` |
| **Agent subagent** | A nested LLM-powered loop that uses tools and returns results | Not yet built |

**Tension**: The Pool expects pre-registered handlers. Agent subagents need runtime-created handlers that depend on the completer. These are different patterns that need different wiring.

**Decision**: Pool subagents are the foundation. Agent subagents are built ON TOP of the Pool — an agent subagent is just a Pool task whose handler creates a mini LLM loop.

### 0.2 Is the Pool Actually Useful for Agent Subagents?

**Yes**, because:
1. Pool handles dependency ordering (task B depends on task A)
2. Pool enforces caps (workers, depth, fanout, timeout)
3. Pool provides context cancellation propagation
4. Pool returns deterministic ordered results
5. Pool deduplicates via idempotency keys

Without the Pool, we'd reimplement all of this in a tool.

### 0.3 One-Shot vs Multi-Step Subagents — The Hardest Decision

| Approach | Cost | Complexity | Usefulness |
|----------|------|-----------|------------|
| **One-shot** (1 LLM call, no tools) | 1 API call per task | Low | Good for summarization, classification, scoped research |
| **Multi-step** (LLM loop with tools) | N API calls per task | High | Full agent capability per subagent |
| **Hybrid** (one-shot first, tools later) | Gradual | Medium | Best path |

**Decision: Start one-shot. Multi-step is Phase 5.** Rationale:

1. **Cost**: One-shot is predictable (1 API call per task). Multi-step could be 10+ calls per subagent. 3 subagents x 5 steps = 15 extra API calls per user message.
2. **Complexity**: Multi-step requires tool registry isolation, scheduler coordination, write-conflict detection, and message history management per subagent. One-shot is a single `ChatStream` call.
3. **User benefit**: One-shot is sufficient for "summarize these files", "find patterns in this code", "compare these approaches". Multi-step is needed for "refactor this module" — which should arguably not be parallelized.
4. **Safety**: One-shot has no tools, so no write conflicts, no command execution risks. Multi-step needs read-only scoping and file locking.

### 0.4 Skills Wiring — Separate Concern or Prerequisite?

Skills (`internal/skills`) are typed Go functions with schemas, permissions, and tool allowlists. They were designed to be pre-registered handlers that the model calls.

**Question**: Should we wire Skills as Subagent handlers in the dispatcher?

| Yes | No |
|-----|-----|
| "dispatch_tasks" with skill names is clean | Skills have a different contract (Version, Permission, InputSchema) |
| Skills already have `RegisterAll()` | Skills are Go functions, not LLM-powered — different thing |
| Skills provide type safety for subagent tasks | The model needs to know skill names and schemas — adds complexity |

**Decision**: Wire skills as Subagent handlers but don't require it for MVP. The initial `dispatch_tasks` tool works with a GENERIC "one-shot LLM" handler that doesn't need pre-registration. Skills as subagent tasks comes in Phase 6.

### 0.5 Circular Dependency: Tool --> Handler --> Completer

The `delegate`/`dispatch_tasks` tool needs to register a subagent handler. But the handler needs the completer (owned by the session). This creates a circular setup:

```
tools.NewDefaultRegistry(ws)  -->  tools.Registry
runtime.NewToolDispatcher(reg)  -->  dispatcher (tools only, no subagent handlers)
chat.NewSession(res, completer) -->  session owns completer
  Need to register subagent handlers that reference completer
  But dispatcher is created before session in current code
```

**Solution**: Add a post-creation wiring step or create everything in one factory function.

### 0.6 Can Subagents Use the Same Tools?

**Short answer: No, for MVP.** One-shot subagents get no tools. The LLM responds based on context from the parent (which can use tools to gather information and pass it to subagents).

**Long answer: Yes, cautiously, for read-only in Phase 5.** When multi-step subagents arrive, read-only scoping (read_file, grep, glob, list_dir, search) is the safe default.

### 0.7 Web Research Summary

Patterns from existing systems:

| System | Pattern | Key Insight |
|--------|---------|-------------|
| **OpenAI Agents SDK** | Agent-as-tool, handoff | Delegate to sub-agent with its own instructions/tools; receive structured results |
| **Microsoft orchestrator** | Supervisor orchestrates workers | Shared context, typed results, bounded concurrency |
| **LangGraph** | Subgraph nodes | Parent graph can spawn sub-graphs with shared state; cancellation propagates |
| **Google Cloud agents** | Fan-out/merge | Independent tasks run in parallel; aggregator merges results |

Commonalities:
- Subagents get their own instructions (system prompt)
- Subagents have limited scope/visibility
- Results are structured (not free text)
- Parent controls cancellation and budgets
- Maximum 2 levels of nesting in practice

### 0.8 Self-Critique of Initial Plan

| What I Got Right | What I Got Wrong |
|-----------------|------------------|
| Phase separation (Foundation -> Task -> Agent) | Overcomplicated Phase 1 with skill subagents |
| Config schema design | Assumed multi-step subagents from the start |
| Pool integration approach | Underestimated circular dependency between handler and completer |
| Safety scopes (read-only vs full) | Missed the one-shot vs multi-step distinction |
| Budget propagation concept | Budget model was too complex for MVP |
| TUI rendering need | Didn't account for handler registration at session level |

---

## 1. Current State Assessment

### What Exists (Standalone)

| Component | File | Completeness |
|-----------|------|-------------|
| `subagents.Pool` | `internal/subagents/subagents.go` | Full DAG executor (bounded workers, deps, idempotency, partial mode, context cancel) |
| `runtime.Dispatcher` with `Subagent` kind | `internal/runtime/dispatcher.go` | Kind constant + handler registration + policy enforcement |
| Skills registry + `RegisterAll()` | `internal/skills/skills.go` | Registration method exists |
| Docs & rules | `.ai/rules/50-concurrency-subagents.md`, `docs/architecture/concurrency.md` | Written |

### What Is Missing (The Gap)

| Gap | Impact | Severity |
|-----|--------|----------|
| No tool exposes subagents to the model | Model cannot call any subagent operation | BLOCKING |
| No subagent handlers registered in dispatcher | `Pool.Run()` -> `d.Invoke()` -> "no handler" | BLOCKING |
| Dispatcher never wired into session | `chat.Session.sendAgent()` passes nil Dispatcher | BLOCKING |
| Skills `RegisterAll()` never called | Skill handlers absent from dispatcher | BLOCKING |
| No config schema for subagents | Users can't tune caps | HIGH |
| No TUI rendering for subagent events | Users can't see parallel progress | MEDIUM |
| No prompt surface for delegation | Model doesn't know it can delegate | MEDIUM |
| No budget/timeout propagation | Parent/child budget chains undefined | MEDIUM |
| No conflict prevention for parallel writes | Two subagents might write same file | MEDIUM |

### Verification

```
grep -r '"github.com/MiviaLabs/mivia-agent/internal/subagents"' *.go **/*.go
--> no matches

grep -r 'skills\.RegisterAll\|skills\.NewRegistry' internal/chat/ internal/agent/ internal/cli/
--> no matches

go test ./internal/subagents/ -v -count=1
--> PASS (3 tests)
```

---

## 2. Architecture

### 2.1 Three Execution Levels

```
Level 0: agent.Loop (parent)
  |-- tool calls (existing: read_file, grep, etc.)
  |-- dispatch_tasks tool  -->  Pool  -->  one-shot LLM handlers  (Phase 1/2)
  |-- delegate tool        -->  Pool  -->  one-shot LLM handler    (Phase 1)

Level 1: subagents.Pool (orchestrator, exists)
  |-- bounded worker goroutines
  |-- dependency ordering (DAG)
  |-- context cancellation propagation
  |-- deterministic result ordering

Level 2: One-shot LLM handler (new, Phase 1)
  |-- Receives task prompt -> single ChatStream call -> structured JSON result
  |-- No tools, no loop, no nested LLM calls
```

No Level 3 yet. Multi-step subagents (with tool access) are deferred to Phase 5.

### 2.2 Dispatcher Wiring

```
Wiring point: cli.configureChatWorkspace() or new newAgentDependencies()

1. tools.Registry = NewDefaultRegistry(ws)
2. runtime.Policy {
     MaxDepth:    config.Subagents.MaxDepth,
     MaxBudget:   config.Subagents.DefaultBudget,
     Sink:        eventSink,
   }
3. runtime.Dispatcher = NewToolDispatcher(reg, policy)
4. Register runtime.Subagent handlers:
   - handler "delegate" -> oneShotSubagentHandler{completer: comp}
     (this is a runtime.Handler that makes one LLM call)
5. Create agent.Loop with dispatcher

Key: The subagent handler is registered AFTER the dispatcher is created,
but it references session-owned objects (completer). This means the
handler must be passed in from the session level.
```

### 2.3 Handler Registration Points

```go
// In chat.Session or a new setup function:

type oneShotHandler struct {
    completer    provider.Completer
    systemPrompt string    // subagent system prompt
}

func (h *oneShotHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
    taskText := string(req.Input)
    msgs := []provider.Message{
        {Role: provider.RoleSystem, Content: h.systemPrompt},
        {Role: provider.RoleUser, Content: taskText},
    }
    // Single LLM call, no tools
    reply, err := h.completer.Chat(ctx, msgs, provider.ChatOptions{
        Temperature: ptr(0.3), // lower temp for focused tasks
    })
    if err != nil {
        return nil, err
    }
    return json.Marshal(map[string]any{
        "output": reply,
        "task":   taskText,
    }), nil
}
```

### 2.4 Tool Surface

#### Tool 1: `delegate` (Phase 1, MVP)

```json
{
  "name": "delegate",
  "description": "Delegate a subtask to a sub-agent. The sub-agent makes one focused LLM call and returns structured results. Use for parallel research, independent analysis, or scoped subtasks that benefit from isolation.",
  "parameters": {
    "type": "object",
    "properties": {
      "task": {
        "type": "string",
        "description": "Natural language task description for the sub-agent"
      }
    },
    "required": ["task"]
  }
}
```

**Why no scope/timeout params?** The model can't accurately predict how long a task takes or what scope it needs. Defaults from config are more reliable. The parent's context cancellation is the safety net.

#### Tool 2: `dispatch_tasks` (Phase 2)

```json
{
  "name": "dispatch_tasks",
  "description": "Execute multiple sub-tasks in parallel as one-shot LLM calls. Each task is a natural language prompt. Use when you need independent analyses that benefit from concurrent execution. Recommended: 2-4 tasks at once.",
  "parameters": {
    "type": "object",
    "properties": {
      "tasks": {
        "type": "array",
        "items": {
          "type": "object",
          "properties": {
            "id": {"type": "string", "description": "Unique task ID (e.g. 't1', 'research_auth')"},
            "prompt": {"type": "string", "description": "Natural language task for the sub-agent"},
            "depends_on": {
              "type": "array",
              "items": {"type": "string"},
              "description": "Task IDs that must complete first"
            }
          },
          "required": ["id", "prompt"]
        },
        "description": "Array of 1-16 tasks. Tasks without depends_on run concurrently."
      }
    },
    "required": ["tasks"]
  }
}
```

### 2.5 Pool as the Execution Engine

Both tools use the same internal flow:

```
Tool.Execute()
  -> Pool.Run(tasks)
    -> validate tasks (caps, cycles, duplicates)
    -> schedule ready tasks -> execute via dispatcher
    -> collect results -> return ordered
```

The Pool is the shared engine. Tools are just JSON adapters.

### 2.6 One-Shot Handler Implementation

```go
// internal/tools/oneshot.go (or internal/subagents/oneshot.go)

type OneShotHandler struct {
    Completer    provider.Completer
    SystemPrompt string
}

func (h *OneShotHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
    taskPrompt := string(req.Input)
    msgs := []provider.Message{
        {Role: RoleSystem, Content: h.SystemPrompt},
        {Role: RoleUser, Content: taskPrompt},
    }
    var reply string
    var err error

    // Apply timeout from request
    callCtx := ctx
    if req.Timeout > 0 {
        var cancel context.CancelFunc
        callCtx, cancel = context.WithTimeout(ctx, req.Timeout)
        defer cancel()
    }

    reply, err = h.Completer.Chat(callCtx, msgs, provider.ChatOptions{
        Temperature: ptr(0.3),
        MaxTokens:   ptr(1024),
    })
    if err != nil {
        return nil, fmt.Errorf("subagent %q: %w", req.Name, err)
    }

    return json.Marshal(map[string]any{
        "output": reply,
        "task":   taskPrompt,
    }), nil
}
```

### 2.7 Budget and Safety Chain

```
Parent Loop
  |  max_steps: 25 (config)
  |  tool_timeout: 60s
  |
  |-- normal tool call (existing)
  |
  |-- delegate/dispatch_tasks
       |-- Pool.Run(tasks)
            |-- Per-task: 1 LLM call, max 30s timeout
            |-- Max parallel: config.MaxWorkers (default 4)
            |-- Total tasks: config.MaxFanout (default 16)
            |-- Depth from parent: config.MaxDepth (default 3)
            |-- Cancellation: parent ctx cancel -> all tasks canceled
```

Key rules:
1. **Budget is per-call, not inherited**: One subagent task = 1 LLM call. No multiplication.
2. **Timeout from config**: Per-task timeout prevents one slow subagent from blocking others.
3. **Context propagates**: Parent cancel = all subagent tasks cancel immediately.
4. **No recursive delegation**: Subagent handlers don't have access to delegate/dispatch_tasks tools (they have no tools at all).
5. **Depth tracking**: The dispatcher tracks depth. If depth > MaxDepth, the handler rejects the call.

### 2.8 File Isolation (Phase 5+)

Not needed for one-shot subagents - they make no filesystem calls. When multi-step subagents arrive:
- Read-only subagents use a restricted tool registry (read/grep/list/search only)
- Write subagents get isolated output directories under `.ai/runs/<run-id>/<task-id>/`

---

## 3. Phased Implementation

### Phase 0: Foundation (1-2 PRs)

**Gate**: All existing tests pass + new unit tests.

#### 0.1 Wire Dispatcher Through Session

**Files**: `internal/chat/session.go`, `internal/cli/chat_repl.go`, `internal/agent/loop.go`

- [ ] Add `Dispatcher *runtime.Dispatcher` field to `chat.Session`
- [ ] In `sendAgent()`, pass `Dispatcher: s.Dispatcher` in `agent.Options{}`
- [ ] In `configureChatWorkspace()`, create and set the dispatcher
- [ ] In `internal/agent/loop.go`, the existing fallback `NewToolDispatcher` already works when Dispatcher is nil - change to only fallback when nil

```go
// In session.sendAgent():
loop := &agent.Loop{
    Completer: s.Completer,
    Tools:     s.Tools,
    Messages:  msgs,
}
// Dispatcher is passed via opts
reply, err := loop.Run(ctx, userText, agent.Options{
    ...
    Dispatcher: s.Dispatcher,
})
```

#### 0.2 Add Subagent Config Section

**Files**: `internal/config/types.go`, `internal/config/defaults.go`, `internal/config/load.go`, `mivia.toml.example`

- [ ] Add `Subagents SubagentConfig` to `File` struct
- [ ] Add `SubagentConfig` struct with tuneables
- [ ] Add `SubagentConfig` to `Resolved` struct
- [ ] Resolve defaults in `Load()`
- [ ] Add example to `mivia.toml.example`

```go
type SubagentConfig struct {
    MaxWorkers       int           `toml:"max_workers"`
    MaxDepth         int           `toml:"max_depth"`
    MaxFanout        int           `toml:"max_fanout"`
    DefaultTimeout   time.Duration `toml:"default_timeout"`
    DefaultBudget    int           `toml:"default_budget"`
    PartialResults   bool          `toml:"partial_results"`
    SystemPrompt     string        `toml:"system_prompt"`
}

const (
    DefaultSubagentWorkers   = 4
    DefaultSubagentDepth     = 3
    DefaultSubagentFanout    = 16
    DefaultSubagentTimeout   = 60 * time.Second
    DefaultSubagentBudget    = 0
    DefaultPartialResults    = false
)
```

#### 0.3 Factory Function for Dispatcher

**Files**: `internal/cli/dispatcher.go` (new)

- [ ] Create `NewSessionDispatcher(reg *tools.Registry, comp provider.Completer, cfg SubagentConfig) *runtime.Dispatcher`
- [ ] Registers tool handlers (via `NewToolDispatcher`)
- [ ] Registers one-shot subagent handler for "delegate" name
- [ ] Sets policy from config

```go
func NewSessionDispatcher(reg *tools.Registry, comp provider.Completer, cfg config.SubagentConfig) *runtime.Dispatcher {
    policy := runtime.Policy{
        MaxDepth:  cfg.MaxDepth,
        MaxBudget: cfg.DefaultBudget,
    }
    d := runtime.NewToolDispatcher(reg, policy)

    // Register the one-shot subagent handler
    handler := &OneShotHandler{
        Completer:    comp,
        SystemPrompt: cfg.SystemPrompt,
    }
    _ = d.Register(runtime.Subagent, "delegate", handler)

    return d
}
```

#### Testing Phase 0

- [ ] `TestDispatcherWiredInSession` -- session creates dispatcher, loop receives it
- [ ] `TestSubagentConfigDefaults` -- config resolves with defaults
- [ ] `TestSendAgentWithDispatcher` -- full session sendAgent works with dispatcher (regression)
- [ ] `TestOneShotHandlerInvoke` -- handler produces JSON result
- [ ] `TestOneShotHandlerTimeout` -- handler respects context timeout
- [ ] `TestOneShotHandlerCancel` -- handler respects context cancel
- [ ] `go test -race ./internal/... -count=1`

---

### Phase 1: `delegate` Tool (1-2 PRs)

**Gate**: Phase 0 merged + tested.

#### 1.1 Create `delegate` Tool

**Files**: `internal/tools/delegate.go` (new), `internal/tools/tools.go` (register)

- [ ] Implement `delegateTool` struct with reference to `*runtime.Dispatcher`
- [ ] Tool name: `delegate`
- [ ] Parameters matching schema in 2.4
- [ ] `Execute()` creates a single `subagents.Task`, calls `pool.Run()`, returns result
- [ ] Register in `NewDefaultRegistry()`

```go
type delegateTool struct {
    dispatcher *runtime.Dispatcher
    cfg        config.SubagentConfig
}

func (t *delegateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    var params struct {
        Task string `json:"task"`
    }
    if err := json.Unmarshal(args, &params); err != nil {
        return "", fmt.Errorf("delegate: %w", err)
    }
    if params.Task == "" {
        return "", fmt.Errorf("delegate: task is required")
    }

    pool := subagents.New(t.dispatcher, subagents.Policy{
        Workers:  t.cfg.MaxWorkers,
        MaxDepth: t.cfg.MaxDepth,
        Timeout:  t.cfg.DefaultTimeout,
    })

    tasks := []subagents.Task{{
        ID:      "delegate-1",
        Name:    "delegate",
        Owner:   "parent",
        Input:   json.RawMessage(`"` + params.Task + `"`),
        Timeout: t.cfg.DefaultTimeout,
    }}

    results, err := pool.Run(ctx, tasks)
    if err != nil {
        return "", fmt.Errorf("delegate: %w", err)
    }
    if len(results) == 0 {
        return `{"status":"no_result"}`, nil
    }
    r := results[0]
    return string(r.Output), r.Err
}
```

#### 1.2 Tool Registration Wiring

The `delegateTool` needs a dispatcher reference. This means `NewDefaultRegistry` needs the dispatcher, or we need a post-creation setup step.

**Decision**: Add the tool after registry creation:

```go
// In configureChatWorkspace() or NewSessionDispatcher():
reg.Register(&delegateTool{dispatcher: d, cfg: resolved.SubagentConfig})
```

This avoids circular dependency: the dispatcher is created first (from tool registry), then delegateTool is added to registry with dispatcher reference.

#### 1.3 Prompt Update

- [ ] Add `delegate` tool description to `defaultAgentPrompt`
- [ ] Include usage guidance

```
- Use `delegate` to offload independent subtasks to a focused sub-agent
- Good for: parallel research, code analysis, summarization
- Each delegate call makes one LLM call with no tools
```

#### 1.4 TUI Rendering (Minimal)

- [ ] Add `EventSubagentStart`, `EventSubagentEnd` to event kinds
- [ ] Render status line: "[delegate] running..." / "[delegate] done (0.8s)"
- [ ] Show elapsed time for running subagent

#### Testing Phase 1

- [ ] `TestDelegateToolRegistered` -- tool in registry
- [ ] `TestDelegateToolSchema` -- JSON schema valid
- [ ] `TestDelegateToolExecutes` -- produces correct result
- [ ] `TestDelegateToolEmptyTask` -- rejects empty task
- [ ] `TestDelegateToolCancel` -- context cancel stops execution
- [ ] `TestDelegateToolEventEmitted` -- start/end events fire
- [ ] `go test -race ./internal/tools/... ./internal/subagents/... -count=1`

---

### Phase 2: `dispatch_tasks` Tool (1-2 PRs)

**Gate**: Phase 1 in production, user feedback collected.

#### 2.1 Create `dispatch_tasks` Tool

**Files**: `internal/tools/dispatch.go` (new), `internal/tools/tools.go` (register)

- [ ] Implement `dispatchTasksTool` struct
- [ ] Tool name: `dispatch_tasks`
- [ ] Parameters matching schema in 2.4
- [ ] `Execute()` creates `subagents.Pool`, runs all tasks, returns aggregated results
- [ ] Registration same pattern as Phase 1.2

```go
type dispatchTasksTool struct {
    dispatcher *runtime.Dispatcher
    cfg        config.SubagentConfig
}

func (t *dispatchTasksTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    var params struct {
        Tasks []struct {
            ID        string   `json:"id"`
            Prompt    string   `json:"prompt"`
            DependsOn []string `json:"depends_on,omitempty"`
        } `json:"tasks"`
    }
    if err := json.Unmarshal(args, &params); err != nil {
        return "", fmt.Errorf("dispatch_tasks: %w", err)
    }
    if len(params.Tasks) == 0 {
        return `{"tasks":[]}`, nil
    }

    pool := subagents.New(t.dispatcher, subagents.PoolPolicy(t.cfg))
    tasks := make([]subagents.Task, len(params.Tasks))
    for i, pt := range params.Tasks {
        tasks[i] = subagents.Task{
            ID:        pt.ID,
            Name:      "delegate", // reuse the one-shot handler
            Owner:     "parent",
            Input:     json.RawMessage(`"` + pt.Prompt + `"`),
            DependsOn: pt.DependsOn,
            Timeout:   t.cfg.DefaultTimeout,
        }
    }

    results, err := pool.Run(ctx, tasks)
    // Serialize ordered results
    out, _ := json.Marshal(results)
    return string(out), err
}
```

#### 2.2 Result Format

Each result includes:

```json
{
  "task_id": "t1",
  "status": "completed",
  "output": "Analysis result text...",
  "duration_ms": 823,
  "error": null
}
```

Or for failed/blocked tasks:

```json
{
  "task_id": "t3",
  "status": "blocked",
  "output": null,
  "error": "dependency t2 failed"
}
```

#### 2.3 Prompt Update

- [ ] Add `dispatch_tasks` to prompt
- [ ] Recommend 2-4 tasks for best results

```
- Use `dispatch_tasks` to run multiple analyses in parallel
- Tasks without dependencies run concurrently
- Use `depends_on` when order matters (e.g. analyze -> summarize)
```

#### 2.4 TUI Grouped Panel

- [ ] Show grouped subagent panel during execution
- [ ] Per-task: status icon, elapsed time, brief preview
- [ ] Update in real-time as tasks complete

```
(delegate 3 tasks) [ok] research_api  0.8s   "Found 12 endpoints..."
                    [...] analyze_auth 1.2s   (running)
                    [...] analyze_db   0.5s   (running)
```

#### Testing Phase 2

- [ ] `TestDispatchToolRegistered` -- tool in registry
- [ ] `TestDispatchToolExecutesTasks` -- pool produces corrent results
- [ ] `TestDispatchToolPartialFailure` -- blocked tasks in partial mode
- [ ] `TestDispatchToolCancelPropagation` -- parent cancel stops all
- [ ] `TestDispatchToolDependencyOrder` -- ordering enforced
- [ ] `TestDispatchToolBudgetEnforcement` -- budget caps work
- [ ] `TestDispatchToolMaxFanout` -- rejects > MaxFanout tasks
- [ ] `go test -race ./internal/subagents/... ./internal/tools/... -count=1`

---

### Phase 3: Observability & UX Polish (1 PR)

#### 3.1 Event System

**Files**: `internal/agent/loop.go`, `internal/cli/toolui.go`

- [ ] Add `EventDelegateStart`, `EventDelegateEnd`, `EventDelegateParallel` to `EventKind`
- [ ] Tools emit events with task ID, status, elapsed
- [ ] TUI renders grouped or stacked

```go
const (
    EventToolParallel      EventKind = "tool_parallel"   // existing
    EventDelegateStart     EventKind = "delegate_start"   // new
    EventDelegateEnd       EventKind = "delegate_end"     // new
    EventDelegateBatch     EventKind = "delegate_batch"   // new
)
```

#### 3.2 TUI Rendering

- [ ] When single delegate: show inline status text
- [ ] When batch dispatch: show grouped panel (see 2.4)
- [ ] Show task dependency arrows (optional, Phase 3+)

#### 3.3 Prompt Finalization

- [ ] Update both `defaultAgentPrompt` and `.ai/agent-prompt.md`
- [ ] Include delegation guidance
- [ ] Emphasize: "Delegate parallel research. Don't do N sequential searches."

---

### Phase 4: Skills Wiring (1 PR)

#### 4.1 Register Skills as Subagent Handlers

**Files**: `internal/skills/skills.go`, `internal/cli/dispatcher.go`

- [ ] Create `RegisterAllAsSubagents(d *runtime.Dispatcher) error` in skills package
- [ ] Register each skill as `Subagent` kind (in addition to `Skill`)
- [ ] Update `NewSessionDispatcher` to call this if skills exist

```go
func (r *Registry) RegisterAllAsSubagents(d *runtime.Dispatcher) error {
    for name := range r.items {
        h, err := r.Handler(name)
        if err != nil {
            return err
        }
        if err := d.Register(runtime.Subagent, name, h); err != nil {
            return err
        }
    }
    return nil
}
```

#### 4.2 Extend `dispatch_tasks` for Named Tasks

- [ ] Add optional `name` field to dispatch_tasks schema alongside `prompt`
- [ ] If `name` is set, look up the pre-registered handler instead of using one-shot
- [ ] If `prompt` is set, use one-shot handler
- [ ] This allows the model to call either skills or free-text tasks

#### Testing Phase 4

- [ ] `TestSkillsRegisteredAsSubagents` -- dispatcher has Subagent handlers
- [ ] `TestDispatchWithSkillName` -- named task uses skill handler
- [ ] `TestDispatchWithPrompt` -- prompt task uses one-shot handler
- [ ] `TestDispatchMixed` -- mix of named and prompt tasks

---

### Phase 5: Multi-Step Subagents (Future, 2-3 PRs)

**Not planned in detail yet. High-level scope:**

- [ ] `agentSubagentHandler` creates mini `agent.Loop` with tools
- [ ] Read-only tool restriction (read_file, grep, glob, list_dir, search only)
- [ ] Per-step timeout and total step cap
- [ ] File-path locking for parallel reads
- [ ] Structured result streaming back to parent
- [ ] Dedicated TUI panel with per-step output

**Gate for Phase 5:**
1. Phase 1-4 in production for 2+ weeks
2. User demand for tool-using subagents validated
3. Performance baseline established (one-shot cost vs benefit)
4. Write-conflict prevention reviewed

---

### Phase 6: Hardening (1 PR, after any phase)

#### 6.1 Concurrency Stress Tests

- [ ] 20 subagent tasks with dependencies
- [ ] Cancel mid-flight, verify all children cancel
- [ ] Race detection (`go test -race`)

#### 6.2 Semgrep Rules

- [ ] Direct `Registry.Execute` calls outside `internal/runtime` -> reject
- [ ] Missing context cancellation in subagent handlers -> warn
- [ ] Unbounded goroutine in subagent code -> reject

#### 6.3 Budget Tracking

- [ ] Track total API calls made by subagents
- [ ] Expose in `/status` command
- [ ] Warn when approaching budget limits

---

## 4. Configuration Schema

### TOML

```toml
[subagents]
max_workers = 4           # goroutine pool size (1-16)
max_depth = 3             # max nesting depth (1-8)
max_fanout = 16           # max tasks per dispatch call (1-64)
default_timeout = "60s"   # per-task wall-clock timeout
default_budget = 0        # max API calls total (0 = unlimited)
partial_results = false   # allow partial results on dependency failure
system_prompt = """       # subagent system prompt (default below)
You are a focused sub-agent. Complete the assigned task concisely.
Report findings as structured bullet points. Do not use tools.
"""
```

### Config Struct

```go
type SubagentConfig struct {
    MaxWorkers     int           `toml:"max_workers"`
    MaxDepth       int           `toml:"max_depth"`
    MaxFanout      int           `toml:"max_fanout"`
    DefaultTimeout time.Duration `toml:"default_timeout"`
    DefaultBudget  int           `toml:"default_budget"`
    PartialResults bool          `toml:"partial_results"`
    SystemPrompt   string        `toml:"system_prompt"`
}
```

### Defaults

```go
var DefaultSubagentConfig = SubagentConfig{
    MaxWorkers:     4,
    MaxDepth:       3,
    MaxFanout:      16,
    DefaultTimeout: 60 * time.Second,
    DefaultBudget:  0,
    PartialResults: false,
    SystemPrompt: `You are a focused sub-agent. Complete the assigned task concisely.
Report findings as structured bullet points. Do not use tools.
Reply with only the analysis results.`,
}
```

---

## 5. Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Token waste: subagent repeats context the parent already has | High | Medium | Subagent prompt says "assume no prior context"; parent should provide necessary context in the task prompt |
| API cost: 5 subagents = 5 extra API calls per user turn | Medium | Medium | Config defaults keep fan-out low (default 16, recommend 2-4). Budget cap (default 0 = unlimited, but configurable) |
| Slow subagent blocks parent: Pool waits for all tasks | Medium | Medium | Per-task timeout (60s default). Partial results mode skips failed deps |
| Model misuses delegation: delegates trivial tasks | Medium | Low | Prompt guidance: "Use delegate for independent scoped work, not simple lookups" |
| Nested delegation depth > 1 | Low | Low | MaxDepth=3 from config, enforced by dispatcher policy |
| User confused by silent parallel work | Medium | Low | TUI panel shows running tasks + elapsed time |
| Subagent LLM hallucinates without tool access | Medium | Medium | Parent provides context in task prompt. Subagent prompt says "do not invent information" |

---

## 6. Open Questions

1. **Should `dispatch_tasks` results be streamed back incrementally, or all at once?**
   All at once is simpler and matches the existing Pool contract. Streaming would require a new result channel API. Decision: all-at-once for MVP, streaming if latency becomes an issue (subagent takes >5s while parent waits).

2. **Should the same Pool instance be reused across multiple `delegate`/`dispatch_tasks` calls?**
   A Pool is cheap to create (just a struct). Creating per-invocation avoids stale state. Decision: new Pool per tool call.

3. **Should subagent results be persisted (e.g. to `.ai/runs/`)?**
   Not needed for one-shot subagents — results are in the conversation history. Persistence matters for long-running multi-step subagents (Phase 5).

4. **How does the provider's rate limit interact with parallel subagents?**
   Pool workers = 4 means at most 4 concurrent API calls. This is below typical rate limits (100+ RPM). If rate limiting becomes an issue, add exponential backoff in the one-shot handler.

5. **Should there be a `/subagents` slash command?**
   For MVP, no — configuration is via TOML file. Add `/subagents status` and `/subagents workers N` in Phase 3.

6. **Should `delegate` provide the subagent's output back to the model as a tool result, or should it be appended to messages?**
   Tool result (current approach). The model sees it as a tool output and can decide what to do with it. This is consistent with how all other tools work.

---

## 7. Implementation Order

```
Phase 0: Foundation           ~1 PR   (dispatcher wiring + config + handler)
  |-- Wire Dispatcher Through Session
  |-- Add Subagent Config
  |-- Factory Function
  |-- Tests

Phase 1: delegate tool        ~1 PR   (single-task subagent)
  |-- delegate tool
  |-- Tool registration wiring
  |-- Prompt update
  |-- Minimal TUI events
  |-- Tests

Phase 2: dispatch_tasks tool  ~1 PR   (multi-task parallel subagent)
  |-- dispatch_tasks tool
  |-- Pool integration
  |-- Result formatting
  |-- TUI grouped panel
  |-- Tests

Phase 3: Observability        ~1 PR   (polish)
  |-- Event system
  |-- TUI rendering
  |-- Prompt finalization

Phase 4: Skills wiring        ~1 PR   (optional enhancement)
  |-- RegisterAllAsSubagents
  |-- Named task dispatch
  |-- Tests

Phase 5: Multi-step           Future  (tool-using subagents)
  |-- Mini agent loop handler
  |-- Read-only restriction
  |-- File locking
  |-- Tests

Phase 6: Hardening            Any time
  |-- Stress tests
  |-- Semgrep rules
  |-- Budget tracking
```

Each phase produces a working, shippable increment. No phase depends on a later phase.

---

## 8. References

- `.ai/rules/50-concurrency-subagents.md` -- product rules for in-process concurrency
- `.ai/plans/tool-skill-before-subagents-plan.md` -- original sequencing decision
- `.ai/plans/tool-skill-before-subagents-tightening-plan.md` -- boundary tightening
- `docs/architecture/concurrency.md` -- concurrency model
- `internal/subagents/subagents.go` -- existing pool implementation
- `internal/runtime/dispatcher.go` -- dispatcher with Subagent kind support
- `internal/skills/skills.go` -- skill registry with RegisterAll
- [Microsoft: Orchestrator and subagent patterns](https://learn.microsoft.com/en-us/agents/architecture/multi-agent-orchestrator-sub-agent)
- [OpenAI: Agent-as-Tool pattern](https://openai.github.io/openai-agents-python/multi_agent/)
- [Google Cloud: Agent design patterns](https://docs.cloud.google.com/architecture/choose-design-pattern-agentic-ai-system)
- [5 multi-agent patterns that work](https://www.digitalapplied.com/blog/multi-agent-orchestration-5-patterns-that-work)
