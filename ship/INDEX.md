# Shipped Agent Instructions — Index

This is the shipped instruction set embedded in the mivia binary.
It is auto-written to `.ai/` when mivia starts in a project without `.ai/`.

For host-specific instructions (developing mivia itself), see the `.ai/` directory in the mivia source repo.

## Read Order

1. `.ai/INDEX.md` (this file)
2. **`.ai/rules/05-adlc-agentic-development-lifecycle.md` — MANDATORY process. Read before any work.**
3. Other `.ai/rules/*.md` as relevant
4. System / tool instructions

## Rules

### ⚠️ MANDATORY — read and follow before any work

`.ai/rules/05-adlc-agentic-development-lifecycle.md` — **ADLC protocol: 7-step engineering cycle for all work. Do not skip.**

### Reference rules (read when relevant)

| File | Purpose |
|------|---------|
| `.ai/rules/00-operating-doctrine.md` | Scope control, docs-first work, idempotency, verification contracts |
| `.ai/rules/01-output-budget.md` | Terse status, final-answer shape, task slicing |
| `.ai/rules/10-security-privacy.md` | Secrets, network, hooks, PII, fail-closed protected actions |
| `.ai/rules/50-concurrency-subagents.md` | Subagents as tasks/goroutines; shared MCP; caps; no process farm |
| `.ai/rules/60-tools-project-language-generic.md` | Model-facing tools + default prompts must be project/language-generic |
| `.ai/rules/70-long-running-heartbeat.md` | Heartbeat protocol for long-running tasks |
| `.ai/rules/80-commit-message.md` | Conventional commit format |

## Doctrines

- `.ai/doctrines/evidence-before-claims.md`
- `.ai/doctrines/verification-is-part-of-delivery.md`
