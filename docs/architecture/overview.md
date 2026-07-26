# Architecture Overview

## Host

- Language: Go
- Entrypoint: `cmd/mivia` -> binary `mivia`
- Libraries: `internal/`
- Module: `github.com/MiviaLabs/mivia-agent`

## Layers

1. **CLI/TUI** - user interaction, streaming display
2. **Orchestrator** - agent loop, subagent scheduler, budgets
3. **Tool gateway** - MCP multiplex, shell pool, allowlists
4. **Providers** - LLM HTTP clients (pooled, rate-limited)

## Subagents

Subagents are in-process concurrent tasks (goroutines), not process farms.
Isolation escalates only when needed (worktree, container, microVM).

Details: `docs/architecture/concurrency.md` and `.ai/rules/50-concurrency-subagents.md`.

## Agent control surface

Policy for coding agents working *on* this repo lives under `.ai/`, not in the product runtime.
See ADR `docs/adr/0002-agent-control-surface.md`.
