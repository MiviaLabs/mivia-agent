# Agent Orchestration Dispatch Protocol & Subagent Hang Prevention Plan

## 1. Incident Summary & Observed Symptoms

During a multi-task subagent dispatch (`dispatch_tasks` with 4 parallel agents):
- Subagents appeared **frozen at Step 1 for 10+ minutes** without emitting tool calls.
- One subagent remained idle for minutes, then eventually completed after an internal timeout.
- Another failed with `llmproxycli: provider error (HTTP 503, type api_error)`.
- The parent orchestrator was completely blocked and unable to inspect (`inspect_agents`), answer parked questions, or intervene.

---

## 2. Root Cause Analysis

### A. Provider Overload & Thundering Herd on Step 1
1. Every subagent defined in `.agents/agents/*.md` specifies `provider: llmproxycli` and heavy models (`claude-sonnet-5`, `claude-opus-5`, `gemini-3.7-flash-high`).
2. When 4 subagents are spawned concurrently, they all immediately send huge payloads (full system instructions, tool definitions, and workspace context) to the local proxy (`llmproxycli`) on **Step 1**.
3. The proxy responds with `HTTP 503 (overloaded / api_error)`.
4. `internal/provider/retry.go` classifies HTTP 503 as retryable and enters an internal exponential backoff retry loop.
5. While retrying against an overloaded proxy during the initial inference turn, no tool calls are emitted. The agent appears frozen at "Step 1" for minutes.

### B. Unbounded Transport & Backstop Timeouts (10m–15m Ceilings)
1. Default HTTP transport timeout is 15 minutes (`http.Client` backstop in `internal/clichat/agent_task_handler.go`).
2. The SDK agentloop backstop is 10 minutes (`internal/sdkadapter/tool_registry.go`).
3. Without a tight per-turn inference deadline, a stalled or queued upstream LLM request will sit blocked for up to 10–15 minutes before erroring.

### C. Parent Blockage under `wait:"run"` + Unanswered Parked Questions
1. `wait:"run"` blocks the parent session for the entire batch duration.
2. If a child agent emits `post_message(kind:"question")`, the child parks waiting for a parent reply (`wait_seconds`, default 180s).
3. Because the parent is blocked in `wait:"run"`, it cannot call `send_to_task` to answer. The child sits idle until its internal timeout expires, then resumes blindly.
4. Because `wait:"run"` returns only the final batch results, no `run_id` is available upfront for live inspection.

---

## 3. The New Operating Protocol

### A. Dispatch Rules
1. **Never use `wait:"run"` for multi-agent batches or tasks requiring iteration.** Always use `wait:"none"`.
2. **Capture `run_id` immediately** on the first turn so the run remains observable and controllable.
3. **Configurable concurrency capped at 3 workers by default (`[subagents] max_workers = 3` in `mivia.toml`) plus spawn stagger (`[subagents] spawn_stagger_ms`, default 150, explicit 0 disables, clamped at 1000; `subagents.Policy.SpawnStagger` delays each batch task's start after the first) to prevent provider throttling and 503 retry freezes.**
4. **Always set explicit `timeout_seconds`** (e.g. `timeout_seconds: 300`) per task. Never rely on the 12-hour default.
5. **Enforce prompt guardrails on every dispatched task prompt** (policy home: rule 50 + shared memory):
   > "Do not park on `question` for non-critical ambiguity. Use best judgment and state assumptions explicitly in your output."

### B. Polling & Monitoring Backoff Schedule
When monitoring a `wait:"none"` run:
1. **Turn 1 (Immediate)**: Poll `list_run_events(run_id, kind="task_blocked")` + `inspect_agents(run_id)`.
2. **Turn 2 (+5s)**: Poll `list_run_events`. If a `task_blocked` event appears, immediately call `send_to_task(kind="answer")`.
3. **Turn 3 (+10s)**: Check `inspect_agents`.
4. **Turn 4 (+20s)**: Check `inspect_agents`.
5. **Turn 5+ (every 30s–60s max)**: Check status until terminal state.

### C. Cancellation & Fail-Fast Heuristic
- If a subagent remains at **Step 1 with zero tool calls for > 90 seconds**, assume provider queue congestion or 503 retry lock:
  1. Call `cancel_run(run_id)`.
  2. Fall back to direct in-session execution (`read_file`, `grep`, `glob`) instead of retrying subagents.

### D. Direct Execution Preference
- For read-only exploration, codebase mapping, and bug searches, **perform direct tool calls in the primary agent turn**.
- Avoid spinning up multiple subagents for work that can be accomplished in 3–5 direct file/grep reads.

---

## 4. Answers to Design & Review Questions

1. **Cancellation Trigger Heuristic**: Fixed at **90 seconds on Step 1** with 0 tool calls emitted, or **2x the estimated task duration**.
2. **Prompt Hygiene Placement**: Recorded as durable policy in `.agents/rules/50-concurrency-subagents.md` (Dispatch Hygiene For Batch Fan-Out), in the shared memory entry, AND in the compiled `MessagingProtocolPrompt` (`internal/subagents/prompts.go`): the kind="question" bullet now instructs children to reserve questions for true blockers and decide small doubts themselves.
3. **Event Polling Scope**: Polling checks both `task_blocked` and terminal states (`task_completed`, `task_failed`, `task_timed_out`).
4. **Cost & Noise Management**: Backoff intervals scale from 5s up to 60s, keeping event querying minimal.
