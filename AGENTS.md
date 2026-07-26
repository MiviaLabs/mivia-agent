# Agent Instructions

Product: **mivia** (MiviaLabs)
Module: `github.com/MiviaLabs/mivia-agent`
Binary: **`mivia`** (`cmd/mivia/`)
Predecessor: `mivia-agentkit` MVP (legacy CLI name mivia-agent; patterns reused, product identity is new)

## Canonical surfaces

1. This file (`AGENTS.md`) — short overview and non-negotiables
2. `.ai/INDEX.md` — control-surface index
3. `.ai/doctrines/*` — evidence and verification doctrines
4. `.ai/rules/*` — durable policy
5. `.ai/skills/*` — workflows
6. `docs/OWNERS.yaml` — doc ownership map
7. Thin adapters only: `CLAUDE.md`, `.claude/`, `.codex/`, `.agents/`, `.github/`

Do not fork policy into adapters. Fix `.ai/` or this file instead.

## Source-of-truth order

1. System / tool instructions
2. `.ai/`
3. `AGENTS.md`
4. Task prompt

## Non-negotiables

- Correctness, security, privacy, maintainability over speed
- No secrets, raw prompts, raw model dumps, or PII in commits/logs/fixtures
- Never bypass Git hooks (`--no-verify`, Husky/Lefthook skip env, etc.)
- Subagents are **tasks/goroutines** with shared pools — not process-per-agent by default
- Update **owned docs only** (`docs/OWNERS.yaml`); no parallel policy docs
- Never claim a check passed unless it was executed
- Ship binary name is `mivia` only

## Local commands

```text
make install-hooks   # once per clone
make verify          # offline gates (config, secrets, docs, contracts, semgrep, go)
make test
make race            # concurrency packages
make build           # produces ./mivia
make secret-scan
make docs-check
make semgrep
```

## Layout

```text
cmd/mivia/           CLI entrypoint -> binary mivia
internal/            Go packages
.ai/                 Canonical agent control surface
docs/                Human docs (OWNERS enforced)
scripts/             Guards, hooks, scans, contract tests
semgrep/             Agent-standards static rules
.githooks/           core.hooksPath entrypoints
```

## Skills (use when relevant)

Ported from **mivia-agent-skills** (keep anti-FP / evidence quality):

| Skill | Role |
|-------|------|
| `engineering-working-contract` | Standing engineering doctrine for all coding work |
| `verify-code-change` | Evidence ladder after code/config changes |
| `bug-audit` | Adversarial confirmed-bug hunt only (no false positives) |

Repo-native:

| Skill | Role |
|-------|------|
| `verify-change` | Mechanical gates + `mivia-report/v1` for scoped changes |
| `docs-update` | OWNERS-safe documentation edits |
| `secure-change` | Auth/secrets/network/tooling review |
| `concurrency-review` | Fan-out, pools, cancel, race |
| `feature-delivery` | Bounded feature slice delivery |

## Doctrines

- `.ai/doctrines/evidence-before-claims.md`
- `.ai/doctrines/verification-is-part-of-delivery.md`

## Git commits

Format (scope **required**):

```text
type(scope): imperative subject
```

Policy SoT: `.ai/policy/commit-message.json`

| Scope | Use for |
|-------|---------|
| `cli` | cmd/mivia, flags, TUI |
| `agent` | orchestrator, subagents, runtime |
| `mcp` | MCP tools/gateway |
| `hooks` | Git + agent tool hooks |
| `ai` | `.ai/` rules, skills, doctrines, policy |
| `docs` | `docs/**`, OWNERS |
| `security` | secrets, privacy, authz |
| `quality` | verify scripts, Semgrep, contract tests |
| `build` | Makefile, go.mod, packaging |
| `ci` | GitHub Actions |
| `test` | tests only |
| `deps` | dependency bumps only |
| `release` | versioning / release process |

There is no `setup` scope. Bootstrap/control-surface work uses `ai`, `hooks`, `quality`, or `build`.

On commit-msg failure the hook prints **allowed types and scopes first**, then the error.

## Completion report

- Outcome
- Changed files
- Verification (commands + results)
- Residual risk / blockers

Formal audits: `.ai/templates/agent-report-v1.md` (`mivia-report/v1`).
Bug-audit: skill-specific finding format only.

## Better than agentkit MVP

| Keep | Improve |
|------|---------|
| Hooks + Semgrep + commit policy | Fewer always-on gates that always run |
| Hook-bypass guard | Docs ownership machine-enforced |
| Conventional commits | Production skills from mivia-agent-skills |
| Control surface under `.ai/` | Binary/product name `mivia`; no skill forks |
