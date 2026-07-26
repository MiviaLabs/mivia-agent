# Agent Workflow

How coding agents must work in this repository.

## Read first

1. `AGENTS.md`
2. `.ai/INDEX.md`
3. `.ai/doctrines/*`
4. Relevant rules and skills

## Standing skills

- Always apply `engineering-working-contract`
- After code changes, apply `verify-code-change`
- For defect hunts, use `bug-audit` (confirmed bugs only)
- For docs, use `docs-update` and `docs/OWNERS.yaml`

## Do

- Smallest change that satisfies the requirement
- Run real checks; never invent pass results
- Update owned docs only
- Keep binary name `mivia`

## Do not

- Bypass hooks
- Create duplicate documentation
- Process-farm subagents by default
- Leave TODO/FIXME/HACK/XXX in committed product or agent config
- Ship any CLI name other than `mivia`

## Completion shape

- Outcome
- Changed files
- Verification (commands + results)
- Risks or blockers
