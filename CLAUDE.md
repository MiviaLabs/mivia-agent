# Claude Code adapter (mivia)

Thin adapter. Canonical instructions:

1. `AGENTS.md`
2. `.mivia/INDEX.md`
3. `.mivia/rules/05-adlc-agentic-development-lifecycle.md` - **ADLC: mandatory process. Read before any work.**
4. `.mivia/doctrines/*`
5. `.mivia/rules/*`
6. `.mivia/skills/*` when relevant

## Product

- Binary: `mivia`
- Module: `github.com/MiviaLabs/mivia-agent`
- Predecessor MVP: mivia-agentkit (do not revive legacy CLI name mivia-agent)

## Standing doctrine

- `.mivia/doctrines/engineering-working-contract.md` for all engineering work
- `architecture-review` at ADLC Step 0, before any plan is locked
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

Do not duplicate policy here. Update `.mivia/` instead.
