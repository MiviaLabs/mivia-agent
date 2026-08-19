# Agent Instructions

Product: **mivia** (MiviaLabs)
Module: `github.com/MiviaLabs/mivia-agent`
Binary: **`mivia`** (`cmd/mivia/`)
Predecessor: `mivia-agentkit` MVP (legacy CLI name mivia-agent; patterns reused, product identity is new)

## Canonical surfaces

1. This file (`AGENTS.md`) - short overview, non-negotiables, and the rules/doctrines index below
2. `.agents/INDEX.md` - fuller control-surface index (skills, policy, quality, hooks, semgrep)
3. `.agents/doctrines/*` - evidence and verification doctrines
4. `.agents/rules/*` - durable policy (linked by title below)
5. `.agents/skills/*` - workflows, symlinked from `.mivia/skills/` (the real tree, and the `mivia` binary's own load path); `.claude/skills/` symlinks the same
6. `docs/OWNERS.yaml` - doc ownership map; ADRs are prohibited
7. Thin adapters only: `CLAUDE.md`, `.claude/`, `.codex/`, `.github/`

Do not fork policy into adapters. Fix `.agents/` or this file instead.

`.mivia/` is scoped to the product's own runtime config and state, not agent
instructions: `mivia.toml` (this repo's own dogfooded config), `workflows/*`
(the workflow engine's definitions, read at runtime by
`internal/tools/workflow_tools.go`), `hooks/` (this repo's lifecycle hook
scripts), `agents/*.toml` (workflow-engine agent role definitions), `policy/*`
(commit-message, pr-title, go-structure, docs-ownership, agent-hook-bypass -
all read by compiled Go code or scripts at a hardcoded `.mivia/policy/` path),
and `skills/` (a required mirror - see point 5). Never move those; they are
functional, not instructional.

### `.agents/memories/`

`.agents/memories/*.md` is team-shared, cross-tool operational memory - facts
and corrected preferences about how to work in THIS repo, not policy -
following the open `.agents` protocol (https://dotagentsprotocol.com/). Each
file uses that protocol's frontmatter (`id`, `title`, `content`, `importance`,
`tags`). It is git-committed, so it is not a substitute for `.agents/rules/*`
(durable policy) or a private per-machine agent memory store; a fact that
becomes a hard rule belongs in `.agents/rules/`, not here.

Read every file under `.agents/memories/` at the start of a task, the same
way you read this file.

## Mandatory process - read before any work

**ADLC (Agentic Development Lifecycle)** is the mandatory engineering process for all feature work, bug fixes, refactors, and cross-package changes in this repo.

Read and follow [`.agents/rules/05-adlc-agentic-development-lifecycle.md`](.agents/rules/05-adlc-agentic-development-lifecycle.md) **before** starting any task.

The ADLC is 7 steps: Plan→Breakdown→Validate→Finalize→Implement (TDD)→Audit→Commit.  
Step 0 requires hostile challenge of the plan before any code is written.  
Step 5 requires hostile bug audit loop until zero bugs found.

**Trivial changes** (≤5 lines, single file, no new types) may use the Fast Path (skip Steps 0-3).  
If unsure whether a change is trivial, use the full ADLC.

## Rules

| Rule | Covers |
|------|--------|
| [00-operating-doctrine](.agents/rules/00-operating-doctrine.md) | Scope control, docs-first work, idempotency, verification contracts |
| [01-output-budget](.agents/rules/01-output-budget.md) | Terse status, final-answer shape, task slicing |
| [05-adlc-agentic-development-lifecycle](.agents/rules/05-adlc-agentic-development-lifecycle.md) | The mandatory 7-step engineering cycle (see above) |
| [10-security-privacy](.agents/rules/10-security-privacy.md) | Secrets, network, hooks, PII, fail-closed protected actions |
| [20-agent-quality](.agents/rules/20-agent-quality.md) | Tests, mutation proofs, review gates, contract coverage |
| [30-go-standards](.agents/rules/30-go-standards.md) | Go layout for `cmd/mivia` + `internal/`, errors, naming, embed |
| [40-docs-ownership](.agents/rules/40-docs-ownership.md) | Single source of truth per topic; no parallel docs; `docs/OWNERS.yaml` |
| [50-concurrency-subagents](.agents/rules/50-concurrency-subagents.md) | Subagents as tasks/goroutines; shared MCP; caps; no process farm |
| [60-tools-project-language-generic](.agents/rules/60-tools-project-language-generic.md) | Generic model-facing tools, default prompts, portable review skill |
| [70-long-running-heartbeat](.agents/rules/70-long-running-heartbeat.md) | Heartbeat protocol for long-running tasks |
| [80-commit-message](.agents/rules/80-commit-message.md) | Conventional commit format |
| [90-writing-standard-ste100](.agents/rules/90-writing-standard-ste100.md) | ASD-STE100 Simplified Technical English for all agent-authored prose |

## Doctrines

| Doctrine | Covers |
|----------|--------|
| [engineering-working-contract](.agents/doctrines/engineering-working-contract.md) | Standing engineering contract |
| [evidence-before-claims](.agents/doctrines/evidence-before-claims.md) | Never claim a check passed unless it ran |
| [verification-is-part-of-delivery](.agents/doctrines/verification-is-part-of-delivery.md) | Verification is not optional cleanup after delivery |

## Source-of-truth order

1. System / tool instructions
2. `.agents/` (rules, doctrines, skills) and `.mivia/` (product runtime config/state)
3. `AGENTS.md`
4. Task prompt

## Non-negotiables

- Correctness, security, privacy, maintainability over speed
- No secrets, raw prompts, raw model dumps, or PII in commits/logs/fixtures
- Never bypass Git hooks (bypass flags, Husky/Lefthook skip env, etc.). Enforced at
  three layers off ONE policy file, `.mivia/policy/agent-hook-bypass.json`: the Git
  hooks themselves, `scripts/agent_hook_guard.py` for adapter agents, and a
  `PreToolUse` lifecycle hook this repo declares in `.mivia/mivia.toml` that refuses
  such a `run_command`. Update the JSON; never fork the patterns into a copy.
- Subagents are **tasks/goroutines** with shared pools - not process-per-agent by default
- Update **owned docs only** (`docs/OWNERS.yaml`); no parallel policy docs
- Never claim a check passed unless it was executed
- All agent-authored prose must use ASD-STE100 Simplified Technical English (STE). See [90-writing-standard-ste100](.agents/rules/90-writing-standard-ste100.md).
- Ship binary name is `mivia` only
- **Model-facing tools + compiled default prompts are project/language-generic** (any user workspace). Host code may be Go; do not bake Go/`cmd/mivia` into tool `Description()` or `defaultAgentPrompt`. Rule: [60-tools-project-language-generic](.agents/rules/60-tools-project-language-generic.md). Enforced by `internal/tools/generic_surface_test.go` and `internal/cli/prompt_generic_test.go`.
- **No spaghetti growth:** prefer files ≤500 LOC and functions ≤80 LOC (hard 800 / 120). Staged files ≤500 KiB. Policy `.mivia/policy/go-structure.json`; gate `scripts/check_go_structure.py` + `file-size-check`. Do not raise baselines to silence failures - split code.

## Local commands

```text
make install-hooks   # once per clone
make verify          # offline gates (config, secrets, docs, contracts, semgrep, go)
make test
make test-changed    # go test on packages with uncommitted/staged .go changes only
make race            # concurrency packages
make build           # produces ./mivia
make secret-scan
make docs-check
make semgrep
```

## Workflow runs

Start every `feature-delivery` run with `scripts/run-delivery-workflow.sh <label>`; the script sets `--allow-publish` and starts the run in the background. It prints the log path.

**Never run a live e2e workflow** (`e2e-split-test`, `e2e-pr-metadata-test`, `e2e-scope-escape-test`), the e2e suite runner, or the context-compaction e2e **without the user explicitly asking for it in that session.** They are not part of `make verify`, CI, or any automated path. Runbook and commands: `docs/development/agent-workflow.md`.

## Layout

```text
cmd/mivia/           CLI entrypoint -> binary mivia
internal/            Go packages
.agents/             Canonical agent control surface (rules, doctrines, skills, quality, templates)
.mivia/              Product runtime config/state: mivia.toml, workflows/, hooks/, agents/*.toml, policy/*
.mivia/hooks/        This repo's own mivia lifecycle hook scripts (project-scoped)
docs/                Human docs (OWNERS enforced)
scripts/             Guards, hooks, scans, contract tests
semgrep/             Agent-standards static rules
.githooks/           core.hooksPath entrypoints
```

## Workflows

When the workspace has `.mivia/workflows/`, the root agent has the workflow
tools by default. `workflow_run` admits and starts a named workflow.
`workflow_status`, `workflow_events`, `workflow_inspect`, and
`workflow_list_runs` observe runs. `workflow_deliver` publishes a
delivery-pending run, but only with `allow_publish=true`. `workflow_cancel`
stops a run. Use the workflow engine when a task fits an existing workflow
definition.

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
| `ai` | `.agents/` rules, skills, doctrines; `.mivia/` policy and workflow config |
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

The report shape (Outcome, Changed files, Verification, Residual risk) is defined in [01-output-budget](.agents/rules/01-output-budget.md). Formal audits: `.agents/templates/agent-report-v1.md` (`mivia-report/v1`). Bug-audit: skill-specific finding format only.
