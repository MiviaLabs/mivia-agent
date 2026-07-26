# ADR 0005: Agent Tools and Loop

## Status

Accepted

## Context

Chat-only mivia cannot edit the repo or run tests. A coding agent needs tools and a tool-call loop.

## Decision

1. OpenAI-compatible `tools` / `tool_calls` on the provider client (non-stream for tool turns).
2. Built-in tools: `read_file`, `list_dir`, `grep`, `glob`, `write_file`, `search_replace`, `run_command`.
3. Workspace root confinement for all filesystem tools.
4. `run_command` uses argv array (no shell), allowlisted binaries, timeout, workspace cwd.
5. Agent loop: model → tool_calls → execute → tool results → model until stop or max steps.
6. CLI: tools **on by default**; `--no-tools` for pure chat.

## Consequences

- mivia can self-improve with go test feedback
- Safety depends on allowlist + path policy (not full sandbox)
