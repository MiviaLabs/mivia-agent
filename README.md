# mivia

Local CLI AI agent from **MiviaLabs**.

| | |
|---|---|
| Product | mivia |
| Binary | `mivia` |
| Module | `github.com/MiviaLabs/mivia-agent` |
| Status | Greenfield successor to the `mivia-agentkit` MVP |

## Quick start

```bash
make install-hooks
make verify
make build
./mivia version
```

## For humans

- Product: `docs/product/overview.md`
- Architecture: `docs/architecture/overview.md`
- Contributing: `docs/contributing.md`
- Hooks: `docs/development/hooks.md`

## For coding agents

Read **`AGENTS.md`** and **`.mivia/INDEX.md`** first.

Canonical skills (ported and improved for reliability):

- `engineering-working-contract` (from mivia-agent-skills)
- `verify-code-change` (from mivia-agent-skills)
- `bug-audit` (from mivia-agent-skills; confirmed bugs only)
- repo skills: `docs-update`, `secure-change`, `concurrency-review`, `feature-delivery`

## Design notes

- Subagents are concurrent tasks with shared pools, not one process per agent.
- Docs ownership is machine-enforced (`docs/OWNERS.yaml`).
- Hooks block secret leaks and verification bypass.
