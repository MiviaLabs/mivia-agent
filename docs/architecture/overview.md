# Architecture Overview

## Host

- Language: Go
- Entrypoint: `cmd/mivia` -> binary `mivia`
- Libraries: `internal/`
- Module: `github.com/MiviaLabs/mivia-agent`

## Layers

1. **CLI** - chat REPL / one-shot; tool event tracing
2. **Agent loop** - tool_calls until stop (`internal/agent`)
3. **Tool gateway** - read/search/edit/run under workspace policy (`internal/tools`)
4. **Workspace** - path confinement (`internal/workspace`)
5. **Providers** - OpenAI-compatible HTTP + tools (`internal/provider`)

See `docs/product/agent.md` and ADR `docs/adr/0005-agent-tools.md`.

Default provider: `deepseek` with model `deepseek-v4-flash` (use `deepseek-v4-pro` for harder tasks).
Config: TOML + env file for secrets. See ADR `docs/adr/0004-provider-config.md` and `docs/product/config.md`.

## Subagents

Subagents are in-process concurrent tasks (goroutines), not process farms.
Isolation escalates only when needed (worktree, container, microVM).

Details: `docs/architecture/concurrency.md` and `.ai/rules/50-concurrency-subagents.md`.

## Agent control surface

Policy for coding agents working *on* this repo lives under `.ai/`, not in the product runtime.
See ADR `docs/adr/0002-agent-control-surface.md`.
