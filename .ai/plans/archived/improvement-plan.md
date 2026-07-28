# Improvement Plan: Agent Harness & Instructions

**Status:** Plan — not yet implemented
**Scope:** Project-agnostic improvements to the mivia agent harness, compiled instructions, and tool descriptions
**Problem:** 25 bugs found across 7 rounds instead of finding them all in 1-2 parallel rounds. dispatch_tasks failed silently, agent fell back to sequential manual work, ADLC audit loop limiter capped at 5 rounds.

---

## Root Causes

### 🔴 Problem 1: dispatch_tasks fails silently with no fallback
When `dispatch_tasks` fails (which happened repeatedly this session), there is **no retry or fallback mechanism**. The agent falls back to sequential `delegate` or manual work, losing all parallelism benefits. The error from dispatch_tasks is opaque — no clear signal whether to retry, use spawn_agent instead, or reduce batch size.

### 🔴 Problem 2: ADLC Step 5 caps audit loops at 5 rounds
Current text: *"Loop until zero bugs OR 5 rounds (→ plan rejected, return to Step 0)."*
This means after 5 audit rounds, even if bugs remain, the plan is rejected. It should be: loop until zero bugs, no artificial cap.

### 🟡 Problem 3: Tool descriptions describe mechanism, not intent
- `dispatch_tasks` Description says "Execute multiple sub-tasks in parallel" — doesn't say "Use this for ALL audits, reviews, and research"
- `spawn_agent` Description says "Spawn a new orchestration run" — doesn't say "Use this for sequential implementation waves with dependency tracking"
- No tool description mentions the ADLC decision tree or tells the agent WHEN to prefer one tool over another

### 🟡 Problem 4: No timeout/retry for dispatch_tasks failures
The dispatch_tasks tool has no built-in retry. If it fails due to a transient error (sub-agent timeout, provider error), the entire batch is lost even with `partial_results: true`.

### 🟡 Problem 5: Agent doesn't inspect/cancel stuck audit agents
Step 5 says to dispatch 3-4 auditors but never tells the agent to monitor them. If one audit agent gets stuck (>2min), there's no instruction to `inspect_agents` and `cancel_run` it.

---

## Proposed Changes

### Change 1: Update embedded ADLC rule (agentkitdata) — project-agnostic

**File:** `agentkitdata/data.go` (embedded `.ai/rules/05-adlc-*.md` shipped to every project)

**What:** Step 5's audit loop cap changed from "5 rounds max" to "loop until zero bugs"

**Current text (line ~220):**
```
3. Loop until zero bugs OR 5 rounds (→ plan rejected, return to Step 0).
```

**New text:**
```
3. Loop until zero bugs. There is NO round limit — keep auditing until all
   auditors report zero bugs. If the same bug keeps reappearing after 3 fix
   attempts, escalate to Step 0 (plan rejected).
```

Also add monitoring instruction:
```
4. While auditors run, periodically call inspect_agents to check progress.
   If any audit agent is stuck >2 minutes, cancel_run it and dispatch a
   replacement. Never let stuck agents delay the loop.
```

---

### Change 2: Update dispatch_tasks Description — project-agnostic tool surface

**File:** `internal/cli/dispatch.go`

**What:** The tool description should tell the agent WHEN to use it, not just WHAT it does.

**Current:**
```
"Execute multiple sub-tasks in parallel through registered subagent or skill handlers. " +
"Each task is a natural language prompt. " +
"Tasks without dependencies (depends_on) run concurrently. " +
"Use when you need independent analyses that benefit from parallel execution. " +
"Recommended: 2-4 tasks at once. " +
...
```

**New:**
```
"Execute 2-4 independent sub-tasks in PARALLEL. Use this for ALL research, code reviews, " +
"bug audits, and any work that can be split — never do N sequential passes. " +
"Tasks without dependencies (depends_on) run concurrently. " +
"Always set handler:\"multi_step\" for tool-using agents and partial_results: true " +
"for audit/challenge rounds (so one failure doesn't lose all results). " +
"Recommended: 2-4 tasks at once. " +
"If dispatch_tasks fails, retry with fewer tasks or switch to spawn_agent. " +
...
```

---

### Change 3: Update spawn_agent Description — project-agnostic

**File:** `internal/cli/orchestrate.go`

**Current:**
```
"Spawn a new orchestration run with one or more agent tasks. " +
"Tasks can declare dependencies (depends_on) for DAG-based execution. " +
...
```

**New:**
```
"Spawn a new orchestration run with one or more agent tasks. " +
"Tasks can declare dependencies (depends_on) for DAG-based execution. " +
"Use spawn_agent when you need sequential execution waves (implement Wave 1, " +
"wait for gate, then Wave 2). For parallel independent tasks, use dispatch_tasks. " +
"Sets wait to control whether the call returns immediately (none), waits for " +
"one task (task), or waits for the full run (run). " +
"Monitor with inspect_agents; cancel with cancel_run. " +
...
```

---

### Change 4: Update delegate Description — project-agnostic

**File:** `internal/cli/delegate.go`

**Current:**
```
"Delegate a subtask to a sub-agent. " +
"By default the sub-agent makes one LLM call (no tools) and returns structured results. " +
...
```

**New:**
```
"Delegate a SINGLE focused subtask to a sub-agent. Use delegate for isolated fixes or " +
"narrow analysis that doesn't need parallelism. For multiple independent tasks, use " +
"dispatch_tasks. For complex multi-step work needing file access, set multi_step=true. " +
"By default the sub-agent makes one LLM call (no tools). " +
...
```

---

### Change 5: Update compiled defaultAgentPrompt — project-agnostic

**File:** `internal/cli/prompt.go`

**What:** Strengthen the parallel execution section to push the agent harder. Add explicit instruction for audit loops and stuck-agent handling.

**Current "Process" section:**
```
# Process — read this first
Read .ai/INDEX.md then .ai/rules/05-adlc-agentic-development-lifecycle.md. The ADLC defines the mandatory 7-step process for ALL work and includes a Tool Reference and Decision Tree telling you exactly which orchestration tool to use and when. Follow it.
```

**New "Process" section — more directive:**
```
# Process — MANDATORY: read and follow
Read .ai/INDEX.md then .ai/rules/05-adlc-agentic-development-lifecycle.md. 
The ADLC is the MANDATORY 7-step engineering process. Follow it exactly.
Step 0: challenge with parallel dispatch_tasks. Step 5: bug audit loop until ZERO bugs.
Do not skip steps. Do not stop audits after N rounds — loop until zero bugs found.
```

**Current "Parallel execution" section:**
```
# Parallel execution — use for ALL non-trivial work
You have tools to run sub-agents in parallel. Use them for research, review, auditing, testing — any work that can be split.
...
Parallelize by default. Do N things at once instead of N sequential passes.
```

**New "Parallel execution" — more aggressive:**
```
# Parallel execution — MANDATORY for all non-trivial work
NEVER do N sequential passes. ALWAYS parallelize research, reviews, audits, testing.
- dispatch_tasks (with partial_results: true) for ALL audits and reviews
- spawn_agent for sequential implementation waves
- delegate only for single focused fixes

If dispatch_tasks fails: retry with fewer tasks, verify handler:"multi_step" is set,
or split into separate spawn_agent calls. Never fall back to sequential work.
```

---

### Change 6: Add retry/resilience to dispatch_tasks tool

**File:** `internal/cli/dispatch.go` — `Execute()` method

**What:** If dispatch_tasks fails with a transient error, automatically retry once with reduced batch size (split into halves). This is project-agnostic resilience.

**Current:** No retry. Error returned directly.

**New:** On transient failure (timeout, provider error), retry once with `len(tasks)/2` per batch. On permanent failure (invalid params), return error immediately.

---

## Implementation Order

| # | Change | File(s) | Effort | Risk |
|---|--------|---------|--------|------|
| 1 | ADLC Step 5 loop cap → unlimited | `agentkitdata/data.go` | 1 line | Low |
| 2 | dispatch_tasks Description | `internal/cli/dispatch.go` | ~10 lines | Low |
| 3 | spawn_agent Description | `internal/cli/orchestrate.go` | ~10 lines | Low |
| 4 | delegate Description | `internal/cli/delegate.go` | ~5 lines | Low |
| 5 | defaultAgentPrompt sections | `internal/cli/prompt.go` | ~10 lines | Low |
| 6 | dispatch_tasks auto-retry | `internal/cli/dispatch.go` | ~30 lines | Medium |

**Total effort:** ~1-2 hours
**Risk:** Low (all changes are to model-facing text or error handling; no new logic bugs possible)

---

## Verification

```
go build ./...                              → PASS
go test -race ./internal/cli/              → PASS (prompt tests + tool surface tests)
go test -race ./internal/tools/            → PASS (generic surface test)
go test -run TestDefaultAgentPrompt        → PASS (size limits + content checks)
go test -run TestToolSurfaceIsProject...   → PASS (no language bias)
```

Gate: All tool descriptions remain project- and language-generic. No Go-specific language in model-facing text.
