# Product Overview

Mivia is a local CLI AI agent for software engineering. The shipped binary is `mivia`.

## Goals

- Interactive CLI/TUI for multi-step agent work
- Extensible tools and capabilities (MCP + in-process tools)
- Reliable multi-subagent fan-out without thrashing a developer laptop
- Deterministic local quality gates (hooks, scans, tests)

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
- Agent instructions: `AGENTS.md` and `.ai/INDEX.md`
