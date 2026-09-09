---
id: dispatch_protocol_hang_prevention
title: Avoid blocking subagent dispatches and prevent Step 1 hangs
content: Never use wait:"run" for multi-agent dispatches; cap batch concurrency to 3, set explicit task timeouts, and fail fast if stuck at Step 1.
importance: high
tags: [orchestration, dispatch_tasks, subagents, concurrency, timeouts]
updated: 2026-09-04
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
