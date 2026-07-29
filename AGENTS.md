# Agent Instructions

Product: **mivia** (MiviaLabs)
Module: `github.com/MiviaLabs/mivia-agent`
Binary: **`mivia`** (`cmd/mivia/`)
Predecessor: `mivia-agentkit` MVP (legacy CLI name mivia-agent; patterns reused, product identity is new)

## Canonical surfaces

1. This file (`AGENTS.md`) — short overview and non-negotiables
2. `.mivia/INDEX.md` — control-surface index
3. `.mivia/doctrines/*` — evidence and verification doctrines
4. `.mivia/rules/*` — durable policy
5. `.mivia/skills/*` — workflows
6. `docs/OWNERS.yaml` — doc ownership map; ADRs are prohibited
7. Thin adapters only: `CLAUDE.md`, `.claude/`, `.codex/`, `.agents/`, `.github/`

Do not fork policy into adapters. Fix `.mivia/` or this file instead.

## Mandatory process — read before any work

**ADLC (Agentic Development Lifecycle)** is the mandatory engineering process for all feature work, bug fixes, refactors, and cross-package changes in this repo.

Read and follow `.mivia/rules/05-adlc-agentic-development-lifecycle.md` **before** starting any task.

The ADLC is 7 steps: Plan→Breakdown→Validate→Finalize→Implement (TDD)→Audit→Commit.  
Step 0 requires hostile challenge of the plan before any code is written.  
Step 5 requires hostile bug audit loop until zero bugs found.

**Trivial changes** (≤5 lines, single file, no new types) may use the Fast Path (skip Steps 0-3).  
If unsure whether a change is trivial, use the full ADLC.

## Source-of-truth order

1. System / tool instructions
2. `.mivia/`
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
- **Model-facing tools + compiled default prompts are project/language-generic** (any user workspace). Host code may be Go; do not bake Go/`cmd/mivia` into tool `Description()` or `defaultAgentPrompt`. Rule: `.mivia/rules/60-tools-project-language-generic.md`. Enforced by `internal/tools/generic_surface_test.go` and `internal/cli/prompt_generic_test.go`.
- **No spaghetti growth:** prefer files ≤500 LOC and functions ≤80 LOC (hard 800 / 120). Staged files ≤500 KiB. Policy `.mivia/policy/go-structure.json`; gate `scripts/check_go_structure.py` + `file-size-check`. Do not raise baselines to silence failures — split code.

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
.mivia/                 Canonical agent control surface
docs/                Human docs (OWNERS enforced)
scripts/             Guards, hooks, scans, contract tests
semgrep/             Agent-standards static rules
.githooks/           core.hooksPath entrypoints
```

## Skills (use when relevant)

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

- `.mivia/doctrines/evidence-before-claims.md`
- `.mivia/doctrines/verification-is-part-of-delivery.md`

## Git commits

Format (scope **required**):

```text
type(scope): imperative subject
```

Policy SoT: `.mivia/policy/commit-message.json`

| Scope | Use for |
|-------|---------|
| `cli` | cmd/mivia, flags, TUI |
| `agent` | orchestrator, subagents, runtime |
| `mcp` | MCP tools/gateway |
| `hooks` | Git + agent tool hooks |
| `ai` | `.mivia/` rules, skills, doctrines, policy |
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

Formal audits: `.mivia/templates/agent-report-v1.md` (`mivia-report/v1`).
Bug-audit: skill-specific finding format only.

## Better than agentkit MVP

| Keep | Improve |
|------|---------|
| Hooks + Semgrep + commit policy | Fewer always-on gates that always run |
| Hook-bypass guard | Docs ownership machine-enforced |
| Conventional commits | Production skills from mivia-agent-skills |
| Control surface under `.mivia/` | Binary/product name `mivia`; no skill forks |
