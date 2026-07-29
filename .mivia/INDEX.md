# .ai Control Surface

Product: **mivia** (MiviaLabs)
Binary: `mivia` (`cmd/mivia/`)
`.mivia/` is the canonical project-level control surface for agentic development in this repo. Root `AGENTS.md` is the canonical instruction file; `.mivia/` holds durable rules, skills, policy, and quality contracts that tool adapters reference.

## Read Order

1. `AGENTS.md`
2. `.mivia/INDEX.md` (this file)
3. **`.mivia/rules/05-adlc-agentic-development-lifecycle.md` — MANDATORY process. Read this before any work.**
4. Relevant other `.mivia/rules/*.md` in numeric order when multiple apply
5. Relevant `.mivia/skills/*/SKILL.md`
6. Relevant `.mivia/policy/*.json` when hooks, commits, or docs ownership are in play
7. Tool adapter files only when running that tool: `CLAUDE.md`, `.agents/`, `.claude/`, `.codex/`, `.github/copilot-instructions.md`

If an adapter conflicts with `AGENTS.md` or `.mivia/`, follow `AGENTS.md` / `.mivia/` and fix the adapter.

## Rules

### ⚠️ MANDATORY — read and follow before any work

`.mivia/rules/05-adlc-agentic-development-lifecycle.md` — **ADLC protocol: 7-step engineering cycle for all work. Do not skip.**
See also "Mandatory process" in `AGENTS.md`.

### Reference rules (read when relevant)

| File | Purpose |
|------|---------|
| `.mivia/rules/00-operating-doctrine.md` | Scope control, docs-first work, idempotency, verification contracts |
| `.mivia/rules/01-output-budget.md` | Terse status, final-answer shape, task slicing |
| `.mivia/rules/10-security-privacy.md` | Secrets, network, hooks, PII, fail-closed protected actions |
| `.mivia/rules/20-agent-quality.md` | Tests, mutation proofs, review gates, contract coverage |
| `.mivia/rules/30-go-standards.md` | Go layout for `cmd/mivia` + `internal/`, errors, naming, embed |
| `.mivia/rules/40-docs-ownership.md` | Single source of truth per topic; no parallel docs; `docs/OWNERS.yaml` |
| `.mivia/rules/50-concurrency-subagents.md` | Subagents as tasks/goroutines; shared MCP; caps; no process farm |
| `.mivia/rules/60-tools-project-language-generic.md` | Model-facing tools + default prompts must be project/language-generic |
| `.mivia/rules/70-long-running-heartbeat.md` | Heartbeat protocol for long-running tasks |
| `.mivia/rules/80-commit-message.md` | Conventional commit format |

## Plans

Active plans follow ADLC protocol (zero `.md` files). Completed plans are archived under `.mivia/plans/archived/`.
Pending (not yet implemented) plans may reside in `.mivia/plans/` temporarily until the ADLC step zero challenge completes.

| File | Status |
|------|--------|
| `.mivia/plans/00-agent-roles-program-overview.md` | 🔄 Program index — see 01-09 |
| `.mivia/plans/01-dispatch-boundary-tool-authorization.md` | ✅ Completed (2026-07-29) — index was stale; the plan header already said so |
| `.mivia/plans/02-run-handle-ownership.md` | ✅ Completed (`402ca3f`) — two test gaps documented in the header |
| `.mivia/plans/03-agentkit-embedded-serving.md` | ❌ CLOSED — `internal/agentkit` + `agentkitdata` deleted; nothing blocked, 04/06 no longer depend on it |
| `.mivia/plans/04-workspace-namespace-mivia.md` | 🔄 Design-ready — **unblocked; no dependencies** |
| `.mivia/plans/05-role-model-core.md` | 🔄 Design-ready — blocked on 01, 04 |
| `.mivia/plans/06-role-skill-binding.md` | 🔄 Design-ready — blocked on 05 |
| `.mivia/plans/07-role-routing.md` | 🔄 Design-ready — blocked on 05 |
| `.mivia/plans/08-role-cli-and-observability.md` | 🔄 Design-ready — blocked on 07 |
| `.mivia/plans/09-role-docs-and-examples.md` | 🔄 Design-ready — blocked on 08 |
| `.mivia/plans/10-configurable-redaction.md` | ✅ Implemented — **redaction is off by default; read §5** |
| `.mivia/plans/11-audit-metadata-honesty.md` | 🔄 Design-ready — **one open decision (§3)**; fields are computed but never read |
| `.mivia/plans/ZAI-GLM-PROVIDER-ADAPTER-PLAN.md` | 🔄 Unregistered — status unknown |
| `.mivia/plans/cli-mvp-standalone.md` | 🔄 BLOCK — not implementation-ready |
| `.mivia/plans/composer-autocomplete.md` | 🔄 Implementation-ready — not started |
| `.mivia/plans/events-eventbus-refactor-plan.md` | 🔄 RFC |
| `.mivia/plans/tui-chat-ux-full-experience.md` | 🔄 Ready — not started |

## Doctrines

- `.mivia/doctrines/evidence-before-claims.md` — from mivia-agent-skills
- `.mivia/doctrines/verification-is-part-of-delivery.md` — from mivia-agent-skills

## Skills

Canonical project skills (under `.mivia/skills/` only; do not fork into tool adapters):

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
| `.mivia/policy/commit-message.json` | Conventional commits: types, scopes, subject length |
| `.mivia/policy/agent-hook-bypass.json` | Blocked verification-bypass flags/env vars + corrective message |
| `.mivia/policy/docs-ownership.json` | Required `docs/OWNERS.yaml`, forbidden duplicate titles, canonical path rules |

## Quality

- `.mivia/quality/contracts/` — project contract matrices for doctor/audit/runtime gates (populate as product surfaces land).

## Runtime Artifacts

- `.mivia/runs/` is for workflow traces and summaries and must be gitignored.
- Never persist raw prompts, raw model outputs, provider payloads, credentials, or plausible secrets under `.mivia/runs/` or elsewhere in the tree.

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
- Agents update the existing canonical document for a topic; they do not create parallel or duplicate docs (see `.mivia/rules/40-docs-ownership.md` and `.mivia/policy/docs-ownership.json`).

## Hooks

- Install: `make install-hooks` (sets `core.hooksPath=.githooks`)
- Implementations: `scripts/git-hooks/*`
- Wrappers: `.githooks/*`
- Agent bypass guard: `scripts/run_agent_hook_guard.sh` + `.mivia/policy/agent-hook-bypass.json`
- Docs: `docs/development/hooks.md`

## Semgrep

- Rules: `semgrep/agent-standards.yml`
- Run: `make semgrep` / pre-commit staged / pre-push full
- Contract: `python3 scripts/test_semgrep_rules.py`

## Verification After Control-Surface Edits

After changing `AGENTS.md`, `.mivia/`, adapter configs, hooks, or Semgrep agent standards:

1. Re-read this INDEX and the touched rule/policy.
2. Run `make verify` (or the narrowest contract test for the change).
3. Report what was verified and what remains unverified.
