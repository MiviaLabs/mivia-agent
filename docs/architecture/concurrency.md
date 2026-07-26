# Concurrency Model

## Constraint

"100 subagents" means 100 logical concurrent tasks with shared pools, not 100 OS processes.

## Defaults (laptop)

| Resource | Cap |
|----------|-----|
| Logical agents | 20–100 |
| In-flight LLM calls | 8–40 (provider quota) |
| Shell workers | 4–16 |
| MCP server processes | 3–12 shared |

## Required mechanisms

- `context.Context` cancellation trees
- Semaphores per resource class
- Bounded mailboxes and tool output size
- Shared token/RPM budgets
- Race tests for concurrent packages (`make race`)

## Forbidden default

Spawning one Python/Node/interpreter process per subagent as the primary fan-out model.

## See also

- `.ai/rules/50-concurrency-subagents.md`
- `.ai/skills/concurrency-review/SKILL.md`
