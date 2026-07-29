# Product Overview

Mivia is a local CLI AI agent for software engineering. The shipped binary is `mivia`.

## Goals

- Interactive CLI chat against LLM providers (DeepSeek default, OpenRouter second)
- Config via TOML + secrets via env file / process env
- Extensible tools and capabilities (MCP + in-process tools) — upcoming
- Reliable multi-subagent fan-out without thrashing a developer laptop — upcoming
- Deterministic local quality gates (hooks, scans, tests)

## First run

```bash
# secrets in .env (repo root or ~/.config/mivia/.env)
# DEEPSEEK_API_KEY=...
make build
./mivia doctor
./mivia chat -p "Hello"
# harder model:
./mivia chat --model deepseek-v4-pro -p "Explain this carefully"
```

Defaults: provider `deepseek`, model `deepseek-v4-flash`. See `docs/product/config.md`.

## Non-goals (initial)

- Hosted multi-tenant control plane (see go-mivia platform separately)
- Replacing every vendor coding agent
- One OS process per subagent as the default runtime model

## Relationship to agentkit

`mivia-agentkit` was the MVP control-surface and CLI experiment (its CLI used a
different name). This repository is the product successor: binary `mivia`,
stricter docs ownership, ported production skills from `mivia-agent-skills`, and
leaner always-on gates.

## See also

- Architecture: `docs/architecture/overview.md`
- Concurrency: `docs/architecture/concurrency.md`
- Agent instructions: `AGENTS.md` and `.mivia/INDEX.md`
