# ADR 0002: Agent Control Surface

## Status

Accepted

## Context

Multiple coding agents (Claude Code, Codex, Copilot, others) need consistent
repository instructions. The agentkit MVP proved `.ai/` + hooks + Semgrep work,
but gates became heavy and skills were fragmented.

## Decision

- Canonical policy: `.ai/` (`INDEX`, rules, doctrines, skills, policy)
- Short adapters: `AGENTS.md`, `CLAUDE.md`, thin tool configs
- Always-on lean gates: config verify, secrets, docs ownership, fmt/test/vet/build, Semgrep, hook contracts
- Port production skills from `mivia-agent-skills` (engineering contract, verify-code-change, bug-audit)

## Consequences

- One place to update policy
- Tool adapters must not fork rules
- `make verify` is the human and agent green bar
