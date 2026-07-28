# Claude Code adapter (mivia)

Thin adapter. Canonical instructions:

1. `AGENTS.md`
2. `.ai/INDEX.md`
3. `.ai/rules/05-adlc-agentic-development-lifecycle.md` — **ADLC: mandatory process. Read before any work.**
4. `.ai/doctrines/*`
5. `.ai/rules/*`
6. `.ai/skills/*` when relevant

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
