# Plan: Agent Instruction Overhaul

**Date:** 2025-07-17
**Status:** Draft
**Priority:** High

## Problem

Every agent prompt in the system is too passive — they describe *what* exists but don't teach *how to think* strategically about tool usage.

| Agent | Current Prompt | Problem |
|-------|---------------|---------|
| **Oneshot subagent** | "Do not use tools. Reply with only results." | Lies/hallucinates when asked to read files — contradiction between "complete the task" and "no tools" |
| **Multi-step subagent** | "You have tools. Complete the task." | No strategic reasoning → wasteful tool calls, wrong tool choices |
| **Main agent** (defaultAgentPrompt) | Lists tools and rules but no decision framework | Doesn't reason about *which* tool *when*, or how to orchestrate subagents |
| **Tool descriptions** | Describe mechanics only | No hints about when to prefer one tool over another |

## Root Cause

The prompts were written as feature descriptions ("here's what exists") rather than strategic instructions ("here's how to think"). Oneshot's "do not use tools" is especially dangerous — it contradicts "complete the task" and forces hallucination.

## Changes

### 1. Oneshot — Honesty & Scope (`internal/subagents/oneshot.go`)

Replace `DefaultSubagentSystemPrompt`:

```go
// Before:
const DefaultSubagentSystemPrompt = `You are a focused sub-agent. Complete the assigned task concisely.
Report findings as structured bullet points. Do not use tools.
Reply with only the analysis results.`

// After:
const DefaultSubagentSystemPrompt = `You are a focused sub-agent with NO tools available.
You cannot read files, list directories, or execute commands.
Answer concisely based only on knowledge already in your training.
If a task requires up-to-date or repo-specific information you cannot know,
state "I cannot answer this without file access" rather than guessing.`
```

### 2. Multi-step — Strategic Thinking (`internal/subagents/multi_step.go`)

Replace the minimal fallback with a rich strategic prompt:

```go
// Before:
subPrompt = "You are a focused sub-agent with access to tools. Complete the assigned task."

// After:
subPrompt = `You are a focused sub-agent with access to tools (read, write, search, run).

# How to work
1. PLAN first — what info do you need, which tool gives it?
2. QUESTION after each result — "do I have enough? Should I try a different tool?"
3. CHAIN tool calls — explore (list_dir) → read (read_file) → search (grep) → repeat as needed
4. REPORT findings as structured data — bullet points, tables, code blocks

# Tool strategy
- Explore structure: list_dir, then read_file on key files
- Find patterns: grep over glob
- Research: search scope=web or search scope=url
- Edit: search_replace over write_file (small edits)
- Read: read_file over run_command
- LAST RESORT: run_command (allowlisted argv only, no shell)

Do not delegate or dispatch — those tools are blocked to prevent infinite recursion.
Use multiple tool calls in sequence. Each call is cheap; guessing is expensive.`
```

### 3. Main Agent — Strategic Orchestrator (`internal/cli/prompt.go`)

Add a strategy section to `defaultAgentPrompt`:

```
# Strategy
- **Plan before acting**: What do you know? What do you need to discover?
- **Parallelize research**: Use dispatch_tasks for 2-4 independent analyses concurrently
- **Delegate depth**: Use delegate with multi_step=true for complex single-thread tasks
- **Quicky no-tool**: Use delegate (default, oneshot) but only for questions answerable from training data
- **Question your approach**: After each step, ask "is this the right tool? do I need more info?"
```

### 4. Default Handler Swap (`internal/cli/dispatch.go`)

- Change default handler from `"oneshot"` to `"multi_step"` (line 121)
- Update tool description to reflect the new default (line 64)

### 5. Tool Description Hints (`internal/tools/search.go`)

Minor: enhance description to hint at scope selection strategy.

### 6. Self-Prompt Update (`.ai/agent-prompt.md`)

Add strategic instruction section to this file so the agent developing mivia benefits from the same thinking patterns.

## Order of Execution

| Step | File | Risk | Impact |
|------|------|------|--------|
| 1 | `internal/subagents/oneshot.go` | Low — oneshot behavior changes (honest refusal vs hallucination) | High — eliminates hallucinated subagent results |
| 2 | `internal/cli/dispatch.go` | Medium — changes default for all dispatch_tasks users | High — multi-step is slower but accurate |
| 3 | `internal/subagents/multi_step.go` | Low-medium — better prompts improve results | Medium — better quality subagent work |
| 4 | `internal/cli/prompt.go` | Low — main agent prompt improvements | High — better orchestrator decisions |
| 5 | `.ai/agent-prompt.md` | Low — self-prompt for this repo | Medium |

## Testing

| Test | What it checks |
|------|----------------|
| `dispatch_tasks` without handler | Uses multi_step, reads real files, returns accurate results |
| `dispatch_tasks handler:"oneshot"` | Honest refusal when asked to read files it can't access |
| `delegate` without multi_step | Honest refusal for file-reading tasks |
| `delegate multi_step=true` | Strategic chaining of tool calls |
| Main agent prompt | Tool descriptions visible, strategic hints present |
| Oneshot with no-tool query (summarize, translate) | Still works correctly for knowledge-based tasks |
