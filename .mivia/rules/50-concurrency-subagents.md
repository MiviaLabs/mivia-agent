# Concurrency And Subagents

Product: **mivia**. Subagents are **tasks**, not OS process farms.

## Mental Model

| Concept | Treat as |
|---------|----------|
| Subagent / worker | In-process task (goroutine + context), or one controlled adapter invocation |
| Long-running tasks | Hours-long orchestration with heartbeats - see .mivia/rules/70-long-running-heartbeat.md
| Shared tools (MCP, repo index, config) | Shared clients/services with explicit concurrency limits |
| Fan-out | Bounded worker pool / errgroup with a hard cap |
| Isolation | Context cancel, scoped workdirs, scrubbed artifacts — not “spawn N shells” |

## Hard Rules

1. **No process farm.** Do not spawn unbounded child processes, nested agent CLIs, or recursive `mivia`/`agent` process trees to “go faster.” One orchestrated level of adapter invocation is enough unless a documented workflow explicitly allows nested runs.
2. **Caps are mandatory.** Every concurrent fan-out declares a maximum concurrency (code constant, config field, or workflow bound). Defaults must be conservative. Unbounded `for { go ... }` over user-sized inputs is forbidden.
3. **Shared MCP / shared services.** MCP servers, indexes, and long-lived clients are **shared** across subagents/tasks in a run. Do not start one MCP server process per subagent. Reuse connections; serialize or pool per server limits.
4. **Context everywhere.** All concurrent work accepts `context.Context` and exits promptly on cancel. Parent cancel aborts children.
5. **No shared mutable state without sync.** Maps, caches, and file writers used by multiple tasks require mutexes, channels, or single-owner designs. Document lock order when multiple locks exist.
6. **Deterministic joins.** Prefer `errgroup` (with `SetLimit`) or an explicit worker pool. First fatal error cancels siblings when partial results are invalid.
7. **Artifact isolation.** Concurrent writers never share the same output file. Use per-task paths under `.mivia/runs/<run-id>/…` then merge deliberately.
8. **Rate and budget.** Token, turn, time, and tool-call budgets apply per run and per child. Children inherit remaining budget; they do not each get a full independent infinite budget.
9. **Host agent parallelism.** When the coding agent fans out subagents for review/implement, keep the same discipline: small fan-out, shared repo tools, no hook-bypass, no parallel commits, no parallel pushes.
10. **Tests.** Concurrent code paths need tests for cancel, cap enforcement, and race-safe outcomes (`go test -race` where applicable).

## Default Caps (until product config overrides)

| Resource | Default |
|----------|---------|
| In-process worker concurrency | ≤ 4 |
| Adapter CLI invocations in parallel | ≤ 2 |
| Nested workflow depth | 1 (no recursive campaign farms) |
| MCP clients per server per run | 1 shared client (pooled requests) |
| Concurrent writers to git index | 1 |

Product config may raise caps only with documentation in the canonical architecture/dev doc and tests for the new bound.

## Subagents As Tasks (Implementation Guidance)

```text
run
 └─ orchestrator (single owner of git + stamp + budgets)
     ├─ task A  ──┐
     ├─ task B  ──┼── shared MCP / shared read-only services
     └─ task C  ──┘
            │
            └─ merge results → single decision → optional protected action
```

- Orchestrator owns protected actions and git mutation.
- Tasks return structured results (paths, findings, status), not raw provider dumps.
- Tasks do not call `git commit`, `git push`, or open PRs.

## Agent Host Behavior (Coding Agents In This Repo)

When using subagents for this codebase:

- Prefer sequential work for edits that touch the same files.
- Parallelize only independent read-only research or disjoint packages.
- Do not start multiple subagents that each launch full agent CLIs against the same dirty worktree for write work.
- Share Mivia MCP / project index context; do not re-ingest the repo per subagent.
- Cap parallel subagents at the defaults above unless the user sets a tighter bound.

## Forbidden Patterns

- `xargs -P` / parallel shells over `mivia` write commands
- One Docker/VM/agent process per file “for isolation” without an explicit product feature
- Background agents left running after the parent finishes
- Concurrent `go test` + formatters that rewrite the same files without coordination
- Bypassing hooks in any child (`--no-verify`, `HUSKY=0`, etc.)

## Skill Coupling

Use skill `concurrency-review` for changes that introduce goroutines, worker pools, adapter parallelism, or multi-agent fan-out. That skill must verify caps, shared MCP usage, cancel paths, and absence of process-farm patterns.
