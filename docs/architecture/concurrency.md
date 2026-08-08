# Concurrency Model

## Constraint

"100 subagents" means 100 logical concurrent tasks with shared pools, not 100 OS processes.

## Defaults (laptop)

| Resource | Cap |
|----------|-----|
| Logical agents | 20–100 |
| In-flight LLM calls | 8–40 (provider quota) |
| Shell workers | 4–16 |
## Async orchestration model

Sub-agents are launched via `spawn_agent` as **DAG tasks** in an orchestration run.
The Coordinator manages the lifecycle asynchronously:

```mermaid
flowchart LR
    spawn_agent --> RunHandle
    RunHandle --> inspect_agents["inspect_agents (poll)"]
    RunHandle --> join_run["join_run (block until terminal)"]
    RunHandle --> cancel_run["cancel_run (two-phase)"]
```

- The Coordinator returns a `RunHandle` immediately - the model can inspect progress, wait, or cancel
- Tasks within a run declare `depends_on` for DAG ordering
- Multiple runs can execute concurrently, each with its own task DAG and state machine
- Run handles are retained for 10 minutes after completion (configurable), then evicted

## Retry model

Failed or timed-out tasks can be retried automatically:

1. Task fails → CAS to `retry_pending`
2. Backoff timer fires → CAS to `queued`
3. DAG scheduler picks it up as ready → CAS to `running`
4. On retry exhaustion → task goes terminal as `failed`

Exponential backoff with jitter prevents thundering herd on retry.

## Required mechanisms

- `context.Context` cancellation trees
- Semaphores per resource class
- Bounded mailboxes and tool output size
- Shared token/RPM budgets
- Race tests for concurrent packages (`make race`)
- **Heartbeat/progress events** for long-running tasks (periodic keep-alive signals emitted by active tasks so callers can detect stalls)
- **Compare-and-set version guards** for concurrent task state transitions (stale-attempt fencing)

## MCP clients

Each selected MCP server has one manager-owned client for a chat session or workflow. The manager discovers a server only when an allowed agent selects it. It serializes discovery and calls for that server. An unavailable server is memoized for the manager lifetime. Cleanup closes HTTP sessions and waits for stdio child processes.

## Forbidden default

Spawning one Python/Node/interpreter process per subagent as the primary fan-out model.

## See also

- Architecture: `docs/architecture/overview.md`
- Agent tools: `docs/product/agent.md`
- [Agent tools and safety](../product/agent.md#safety-and-limits)
