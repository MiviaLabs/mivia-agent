# Claude Code adapter (mivia)

Thin adapter. Canonical instructions:

1. `AGENTS.md`
2. `.ai/INDEX.md`
3. `.ai/doctrines/*`
4. `.ai/rules/*`
5. `.ai/skills/*` when relevant

## Product

- Binary: `mivia`
- Module: `github.com/MiviaLabs/mivia-agent`
- Predecessor MVP: mivia-agentkit (do not revive legacy CLI name mivia-agent)

## Standing skills

- `engineering-working-contract` for all engineering work
- `verify-code-change` after code changes
- `bug-audit` only for adversarial defect hunts

## Commands

```bash
make install-hooks
make verify
make test
make build
```

## Hooks

Project hooks go through `scripts/run_agent_hook_guard.sh`. Do not bypass Git hooks.

Do not duplicate policy here. Update `.ai/` instead.
