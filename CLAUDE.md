# Claude Code adapter (mivia)

@AGENTS.md

Thin adapter only. Do not duplicate policy here - update `AGENTS.md` or `.mivia/` instead.

## Claude Code specifics

- Project hooks route through `scripts/run_agent_hook_guard.sh`. Do not bypass Git hooks.
- Standing doctrine to apply on every task: `.mivia/doctrines/engineering-working-contract.md`.
- Run `architecture-review` at ADLC Step 0, before any plan is locked.
- Run `verify-code-change` after code changes; `bug-audit` only for adversarial defect hunts.
