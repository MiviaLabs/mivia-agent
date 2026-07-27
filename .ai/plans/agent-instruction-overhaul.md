# Plan: Agent Instruction Overhaul (Production-Ready)

**Date:** 2025-07-17
**Status:** Production-Ready
**Priority:** High

## Problem

Every agent prompt in the system is too passive — they describe *what* exists but don't teach *how to think* strategically about tool usage.

| Agent | Current Prompt | Problem |
|-------|---------------|---------|
| **Oneshot subagent** | "Do not use tools. Reply with only results." | Lies/hallucinates when asked to read files — contradiction between "complete the task" and "no tools" |
| **Multi-step subagent** | "You have tools. Complete the task." (fallback) | **Critical bug**: actually gets oneshot prompt ("Do not use tools") from dispatcher — see Bug #1 |
| **Main agent** (defaultAgentPrompt) | Lists tools and rules but no decision framework | Doesn't reason about *which* tool *when*, or how to orchestrate subagents |
| **Tool descriptions** | Describe mechanics only | No hints about when to prefer one tool over another |

## Bugs Found During Validation

### 🐛 Bug #1 (CRITICAL): Multi-step subagent gets "Do not use tools" prompt

**File:** `internal/cli/dispatcher.go`, lines 42-63

```go
// Line 42-44: One shared sysPrompt variable
sysPrompt := cfg.SystemPrompt
if sysPrompt == "" {
    sysPrompt = subagents.DefaultSubagentSystemPrompt  // "Do not use tools"
}

// Line 46-51: Oneshot handler — correct, no tools
handler := &subagents.OneShotHandler{
    SystemPrompt: sysPrompt,  // ✓ correct
}

// Line 54-63: Multi-step handler — SAME prompt, WRONG
multiStepHandler := &subagents.MultiStepHandler{
    SystemPrompt: sysPrompt,  // ✗ "Do not use tools" — blocks tools!
}
```

**Impact:** Setting `multi_step=true` on `delegate` or using `handler:"multi_step"` in `dispatch_tasks` creates a sub-agent that has tool access but is told not to use them. The agent may still use tools (LLMs don't always follow instructions perfectly), but results are unreliable.

**Fix:** Multi-step handler must get its own prompt, not share the oneshot prompt.

### 🐛 Bug #2 (MEDIUM): Oneshot prompt forces hallucination

The oneshot prompt says "Do not use tools" AND "Complete the assigned task concisely." When the task asks "read this file" or "explore the codebase," the agent faces a contradiction and hallucinates rather than refusing.

**Fix:** Replace with honest scope boundaries (see Change 1).

## Root Cause

Prompts were written as feature descriptions ("here's what exists") rather than strategic instructions ("here's how to think"). The shared prompt variable in `dispatcher.go` made the bug invisible during initial development.

## Changes

### 1. Separate Prompt Constants (`internal/subagents/oneshot.go` + `internal/subagents/prompts.go` new)

Extract prompts into a dedicated file for discoverability and independent evolution.

**`internal/subagents/prompts.go` (new):**

```go
package subagents

// OneshotSystemPrompt is for sub-agents with NO tools — single LLM call only.
// Must define a clear positive scope so the agent knows when to answer vs refuse.
const OneshotSystemPrompt = `You are a focused sub-agent with NO tools available.
You cannot read files, list directories, or execute commands.

## What you CAN do
Answer from general knowledge only: definitions, translations, summaries of
well-known concepts, explanations of standard patterns, language syntax, etc.

## What you CANNOT do
- Read files or directories
- Search code or the web
- Execute commands
- Give repo-specific answers (file contents, function signatures, project structure)

If a task requires information you cannot access, state clearly:
"I cannot answer this without file access."
Do NOT guess or invent.`

// MultiStepSystemPrompt is for sub-agents with full tool access (agent loop).
// Principle-based rather than recipe-based to avoid overfitting.
const MultiStepSystemPrompt = `You are a focused sub-agent with access to tools: read_file, list_dir, grep, glob, write_file, search_replace, run_command, and search (local/web/url).

## Principles
1. **Target first** — Prefer precise tools (grep for a pattern, read_file for a known path) over broad exploration (list_dir everything).
2. **Question results** — After each tool call, ask: "Do I have enough? Should I try a different tool or angle?"
3. **Chain efficiently** — Use 1-2 calls for simple lookups. Chain more only if the task genuinely requires multiple discovery steps.
4. **Stop when done** — When you have concrete evidence to answer the task, report it. Do not keep exploring.

## Tool guidance
- **read_file** — reading file contents (prefer over run_command cat)
- **list_dir** — exploring directory structure
- **grep** — finding patterns in code/text (prefer over run_command grep)
- **glob** — finding files by name pattern (prefer over shell find)
- **write_file** — creating/overwriting files (prefer search_replace for small edits)
- **search_replace** — precise surgical edits
- **run_command** — LAST RESORT for tests, builds, git (allowlisted argv only, no shell)
- **search scope=web** — research topics online
- **search scope=url** — fetch specific URL contents
- **search scope=local** — combined grep+glob

## Blocked
delegate and dispatch_tasks are blocked to prevent infinite recursion.

Report findings as structured data: bullet points, tables, code blocks.`
```

### 2. Fix Bug #1: Separate prompts in dispatcher (`internal/cli/dispatcher.go`)

Change lines 42-63 to use separate prompts:

```go
// Oneshot handler — uses the no-tools prompt
sysPrompt := cfg.SystemPrompt
if sysPrompt == "" {
    sysPrompt = subagents.OneshotSystemPrompt
}

// Multi-step handler — uses the tools-enabled prompt
multiSysPrompt := cfg.SystemPrompt
if multiSysPrompt == "" {
    multiSysPrompt = subagents.MultiStepSystemPrompt
}

multiStepHandler := &subagents.MultiStepHandler{
    SystemPrompt: multiSysPrompt,  // ✓ now uses the right prompt
}
```

### 3. Update Oneshot Fallback in multi_step.go (`internal/subagents/multi_step.go`)

The current fallback on line 81:
```go
subPrompt = "You are a focused sub-agent with access to tools. Complete the assigned task."
```

This is fine as a last-resort fallback (only used if SystemPrompt is empty AND we somehow didn't set it). Keep it but could use `MultiStepSystemPrompt` as fallback directly.

### 4. Default Handler Swap (`internal/cli/dispatch.go`)

**Lines 119-121:** Change default handler from `"oneshot"` to `"multi_step"`:

```go
handler := pt.Handler
if handler == "" {
    handler = "multi_step"  // was "oneshot"
}
```

**Line 64:** Update tool description:
```go
"description": "Registered subagent or skill handler; defaults to multi_step (tools enabled)",
```

**Tradeoff:** Multi-step is slower (full agent loop, multiple LLM calls) but accurate. Oneshot is fast but limited to knowledge-only questions.

### 5. Main Agent — Strategic Orchestrator (`internal/cli/prompt.go`)

Add strategy section to `defaultAgentPrompt`:

```
# Strategy
- **Plan before acting**: What do you already know? What needs discovery?
- **Parallelize**: Use dispatch_tasks for 2-4 independent analyses concurrently
- **Delegate depth**: Use delegate with multi_step=true for complex single-thread tasks
- **Oneshot for knowledge**: Use delegate (default) only for questions answerable from training data
- **Question your approach**: After each step, ask "is this the right tool? do I need more info?"
- **Verify**: Run project's own tests/build after changes
```

### 6. Tool Descriptions — Strategic Hints (`internal/tools/search.go`)

Enhance `search` tool description:
```
// Before:
"Unified search: scope=local (grep & glob files), scope=web (web search...), scope=url (fetch and read URL contents)."

// After:
"Unified search: scope=local for file content search (grep+glob), scope=web for internet research, scope=url to fetch a specific page. Prefer scope=local for code queries."
```

### 7. Self-Prompt Update (`.ai/agent-prompt.md`)

Update this repo's own prompt to include strategic thinking guidance.

## Order of Execution

| Step | File(s) | Risk | Impact | Test Impact |
|------|---------|:----:|:------:|:-----------:|
| **1** | `internal/subagents/prompts.go` (new) | Low | Foundational — extracts prompts | No existing tests reference these constants yet |
| **2** | `internal/subagents/oneshot.go` | Low | Remove old const, reference new one | Check tests that import `DefaultSubagentSystemPrompt` |
| **3** | `internal/cli/dispatcher.go` | **Medium** | Fixes Bug #1 — multi-step gets right prompt | Check tests using `NewSessionDispatcher` |
| **4** | `internal/cli/dispatch.go` | **Medium** | Default swap oneshot→multi_step | Check dispatch_tasks tests (may need update) |
| **5** | `internal/subagents/multi_step.go` | Low | Fallback prompt update | Check multi_step tests |
| **6** | `internal/cli/prompt.go` | Low | Strategy section in main prompt | Check prompt tests |
| **7** | `internal/tools/search.go` | Low | Description update | No test impact |
| **8** | `.ai/agent-prompt.md` | Low | Self-update | No test impact |

## Rollback Strategy

| Scenario | Rollback Action |
|----------|----------------|
| Multi-step agents stop using tools entirely | Revert Step 3 (`dispatcher.go`) — multi-step goes back to shared prompt |
| dispatch_tasks too slow with multi-step default | Revert Step 4 (`dispatch.go`) — revert to oneshot default |
| Prompt conflicts or contradictions | Revert individual prompt files — each is a self-contained const |

## Testing Plan

| Test | What It Validates |
|------|-------------------|
| `go test ./internal/subagents/...` | Prompt constants exist, no compilation errors |
| `go test ./internal/cli/...` | Dispatcher wiring still works, handlers resolve correctly |
| Manual: `delegate(task="summarize Go channels")` | Oneshot still answers knowledge questions |
| Manual: `delegate(task="list files in internal/")` | Oneshot honestly refuses |
| Manual: `delegate(task="list files in internal/", multi_step=true)` | Multi-step reads files, returns real results |
| Manual: `dispatch_tasks` with 2 file-reading tasks | Both run in parallel with multi-step, read real files |
| Manual: `dispatch_tasks handler:"oneshot"` with file task | Honest refusal |

## Performance Considerations

| Aspect | Oneshot (current default) | Multi-step (proposed default) |
|--------|:-------------------------:|:-----------------------------:|
| LLM calls per task | 1 | 2-8 (variable) |
| Latency | ~1-3s | ~5-30s |
| Token cost | Low | Moderate-High |
| Accuracy for file tasks | ❌ Hallucinates | ✅ Real |
| Accuracy for knowledge tasks | ✅ | ✅ (but wasteful) |

**Recommendation:** Default to multi_step but document that `handler:"oneshot"` exists for quick knowledge queries.
