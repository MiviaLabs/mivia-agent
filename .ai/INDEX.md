# .ai Control Surface

Product: **mivia** (MiviaLabs)
Binary: `mivia` (`cmd/mivia/`)
`.ai/` is the canonical project-level control surface for agentic development in this repo. Root `AGENTS.md` is the canonical instruction file; `.ai/` holds durable rules, skills, policy, and quality contracts that tool adapters reference.

## Read Order

1. `AGENTS.md`
2. `.ai/INDEX.md` (this file)
3. **`.ai/rules/05-adlc-agentic-development-lifecycle.md` — MANDATORY process. Read this before any work.**
4. Relevant other `.ai/rules/*.md` in numeric order when multiple apply
5. Relevant `.ai/skills/*/SKILL.md`
6. Relevant `.ai/policy/*.json` when hooks, commits, or docs ownership are in play
7. Tool adapter files only when running that tool: `CLAUDE.md`, `.agents/`, `.claude/`, `.codex/`, `.github/copilot-instructions.md`

If an adapter conflicts with `AGENTS.md` or `.ai/`, follow `AGENTS.md` / `.ai/` and fix the adapter.

## Rules

### ⚠️ MANDATORY — read and follow before any work

`.ai/rules/05-adlc-agentic-development-lifecycle.md` — **ADLC protocol: 7-step engineering cycle for all work. Do not skip.**
See also "Mandatory process" in `AGENTS.md`.

### Reference rules (read when relevant)

| File | Purpose |
|------|---------|
| `.ai/rules/00-operating-doctrine.md` | Scope control, docs-first work, idempotency, verification contracts |
| `.ai/rules/01-output-budget.md` | Terse status, final-answer shape, task slicing |
| `.ai/rules/10-security-privacy.md` | Secrets, network, hooks, PII, fail-closed protected actions |
| `.ai/rules/20-agent-quality.md` | Tests, mutation proofs, review gates, contract coverage |
| `.ai/rules/30-go-standards.md` | Go layout for `cmd/mivia` + `internal/`, errors, naming, embed |
| `.ai/rules/40-docs-ownership.md` | Single source of truth per topic; no parallel docs; `docs/OWNERS.yaml` |
| `.ai/rules/50-concurrency-subagents.md` | Subagents as tasks/goroutines; shared MCP; caps; no process farm |
| `.ai/rules/60-tools-project-language-generic.md` | Model-facing tools + default prompts must be project/language-generic |

## Doctrines

- `.ai/doctrines/evidence-before-claims.md` — from mivia-agent-skills
- `.ai/doctrines/verification-is-part-of-delivery.md` — from mivia-agent-skills

## Skills

Canonical project skills (under `.ai/skills/` only; do not fork into tool adapters):

Ported from **mivia-agent-skills** (higher reliability than agentkit MVP copies):

- `engineering-working-contract` — standing communication, evidence, engineering, verification
- `verify-code-change` — blast-radius verification ladder; PASS/PARTIAL/FAIL
- `bug-audit` — confirmed reachable bugs only; hard anti-false-positive rules

Repo-native:

- `verify-change` — mechanical package/gates report via `mivia-report/v1`
- `docs-update` — OWNERS-safe documentation edits; no duplicates
- `secure-change` — secrets, authz, network, tool isolation
- `concurrency-review` — subagent caps, pools, cancel, race
- `feature-delivery` — bounded feature slice with verification

`bug-audit` remains report-only. It does not commit or push.

## Policy

Machine-readable hook and agent policy:

| File | Purpose |
|------|---------|
| `.ai/policy/commit-message.json` | Conventional commits: types, scopes, subject length |
| `.ai/policy/agent-hook-bypass.json` | Blocked verification-bypass flags/env vars + corrective message |
| `.ai/policy/docs-ownership.json` | Required `docs/OWNERS.yaml`, forbidden duplicate titles, canonical path rules |

## Quality

- `.ai/quality/contracts/` — project contract matrices for doctor/audit/runtime gates (populate as product surfaces land).

## Templates And Schemas

- `.ai/templates/` — report and plan templates for skills.
- `.ai/schemas/` — JSON schemas for machine-readable plan/report artifacts.

## Runtime Artifacts

- Active plans live under `.ai/plan/<name>/` with evidence, audit logs, and done marker.
- `.ai/plans/` (plural) is deprecated — new work uses `.ai/plan/` (singular).
- `.ai/runs/` is for workflow traces and summaries and must be gitignored.
- Never persist raw prompts, raw model outputs, provider payloads, credentials, or plausible secrets under `.ai/runs/` or elsewhere in the tree.

## Product Commands (once Go lands)

```bash
go test ./...
go vet ./...
go build -o bin/mivia ./cmd/mivia
./bin/mivia --help
```

Use binary name **`mivia`** only. Do not invent or document a `mivia-agent` binary for this product.

## Documentation Ownership

- Topic ownership and canonical paths are declared in `docs/OWNERS.yaml`.
- Agents update the existing canonical document for a topic; they do not create parallel or duplicate docs (see `.ai/rules/40-docs-ownership.md` and `.ai/policy/docs-ownership.json`).

## Hooks

- Install: `make install-hooks` (sets `core.hooksPath=.githooks`)
- Implementations: `scripts/git-hooks/*`
- Wrappers: `.githooks/*`
- Agent bypass guard: `scripts/run_agent_hook_guard.sh` + `.ai/policy/agent-hook-bypass.json`
- Docs: `docs/development/hooks.md`

## Semgrep

- Rules: `semgrep/agent-standards.yml`
- Run: `make semgrep` / pre-commit staged / pre-push full
- Contract: `python3 scripts/test_semgrep_rules.py`

## Verification After Control-Surface Edits

After changing `AGENTS.md`, `.ai/`, adapter configs, hooks, or Semgrep agent standards:

1. Re-read this INDEX and the touched rule/policy.
2. Run `make verify` (or the narrowest contract test for the change).
3. Report what was verified and what remains unverified.
