# Copilot instructions (mivia)

Follow `AGENTS.md` and `.agents/INDEX.md`.

- Product binary: `mivia` (not `mivia-agent`)
- Module: `github.com/MiviaLabs/mivia-agent`
- Do not bypass Git hooks
- Update docs only via `docs/OWNERS.yaml` ownership
- Subagents are tasks with shared pools, not process-per-agent
- Before locking a plan, apply `.mivia/skills/architecture-review/SKILL.md` (ADLC Step 0)
- After code changes, apply verification from `.mivia/skills/verify-code-change/SKILL.md`
- Standing engineering contract: `.agents/doctrines/engineering-working-contract.md`
