# Claude Code adapter (mivia)

@AGENTS.md

Thin adapter only. Do not duplicate policy here - update `AGENTS.md` or `.agents/` instead.

## Claude Code specifics

- Project hooks route through `scripts/run_agent_hook_guard.sh`. Do not bypass Git hooks.
- Standing doctrine to apply on every task: `.agents/doctrines/engineering-working-contract.md`.
- For substantial feature work, use the `delivery` skill (its Step 0 runs
  `architecture-review` before any plan is locked). Small, well-understood
  changes do not need it.
- Run `verify-code-change` after code changes; `bug-audit` only for adversarial defect hunts.
