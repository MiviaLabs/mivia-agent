---
id: dispatch_protocol_hang_prevention
title: Avoid blocking subagent dispatches and prevent Step 1 hangs
content: Never use wait:"run" for multi-agent dispatches; cap batch concurrency to 3, set explicit task timeouts, and fail fast if stuck at Step 1.
importance: high
tags: [orchestration, dispatch_tasks, subagents, concurrency, timeouts]
archived_on: 2026-09-11
updated: 2026-09-11
---

# Subagent Dispatch & Hang Prevention Protocol

## Context & Pitfalls
1. Using `dispatch_tasks` with `wait:"run"` blocks the parent turn. When a child agent asks a question or gets stuck, the parent cannot inspect or intervene.
2. Spawning 3+ subagents simultaneously using heavy models (`llmproxycli`) floods local proxy and provider endpoints on Step 1, triggering `HTTP 503 (overloaded)` errors and deep retry loops that appear as 10-minute freezes.

## Required Practice
1. **Always use `wait:"none"`** for multi-agent or iterative batches. Capture `run_id` immediately.
2. **Configurable concurrency capped at 3 workers by default (`[subagents] max_workers = 3` in `mivia.toml`)** to protect local proxies/rate-constrained providers.
3. **Always pass an explicit `timeout_seconds`** per task (e.g. 180s–300s).
4. **Enforce prompt guardrail**: "Do not park on `question` for non-critical ambiguity. Use best judgment and state assumptions explicitly."
5. **Fail-fast on Step 1**: If a subagent stays at Step 1 with 0 tool calls for > 90 seconds, cancel the run (`cancel_run`) and fall back to direct file inspection tools.

## Archive note
Archived on 2026-09-11 at the operator's direction ("delete this memory") during the
`memories-housekeeping` audit. The audit could not confirm the blanket claim in rule 1:
a 3-task `wait:"run"` batch completed in 236s with all three results delivered in the
same session, and the orchestrator guidance in the agent prompt recommends `wait:"run"`
for dependent waves. The operator chose removal over narrowing an unverified rule.

Two parts of this file were independently confirmed TRUE and are NOT contradicted by the
audit, in case they need recovering:
- `[subagents] max_workers = 3` is live config (.mivia/mivia.toml:468,475).
- Explicit per-task `timeout_seconds` and fail-fast-on-Step-1 remain sound practice.

The orphan check gained no inbound reference from this file; nothing linked to it.
