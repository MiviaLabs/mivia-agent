# Agent Instructions

Product: **mivia** (MiviaLabs)
Module: `github.com/MiviaLabs/mivia-agent`
Binary: **`mivia`** (`cmd/mivia/`)
Predecessor: `mivia-agentkit` MVP (legacy CLI name mivia-agent; patterns reused, product identity is new)

## Canonical surfaces

1. This file (`AGENTS.md`) - short overview and non-negotiables
2. `.mivia/INDEX.md` - control-surface index
3. `.mivia/doctrines/*` - evidence and verification doctrines
4. `.mivia/rules/*` - durable policy
5. `.mivia/skills/*` - workflows
6. `docs/OWNERS.yaml` - doc ownership map; ADRs are prohibited
7. Thin adapters only: `CLAUDE.md`, `.claude/`, `.codex/`, `.agents/`, `.github/`

Do not fork policy into adapters. Fix `.mivia/` or this file instead.

## Mandatory process - read before any work

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
- Never bypass Git hooks (bypass flags, Husky/Lefthook skip env, etc.). Enforced at
  three layers off ONE policy file, `.mivia/policy/agent-hook-bypass.json`: the Git
  hooks themselves, `scripts/agent_hook_guard.py` for adapter agents, and a
  `PreToolUse` lifecycle hook this repo declares in `.mivia/mivia.toml` that refuses
  such a `run_command`. Update the JSON; never fork the patterns into a copy.
- Subagents are **tasks/goroutines** with shared pools - not process-per-agent by default
- Update **owned docs only** (`docs/OWNERS.yaml`); no parallel policy docs
- Never claim a check passed unless it was executed
- All agent-authored prose must use ASD-STE100 Simplified Technical English (STE). See the Writing standard section.
- Ship binary name is `mivia` only
- **Model-facing tools + compiled default prompts are project/language-generic** (any user workspace). Host code may be Go; do not bake Go/`cmd/mivia` into tool `Description()` or `defaultAgentPrompt`. Rule: `.mivia/rules/60-tools-project-language-generic.md`. Enforced by `internal/tools/generic_surface_test.go` and `internal/cli/prompt_generic_test.go`.
- **No spaghetti growth:** prefer files ≤500 LOC and functions ≤80 LOC (hard 800 / 120). Staged files ≤500 KiB. Policy `.mivia/policy/go-structure.json`; gate `scripts/check_go_structure.py` + `file-size-check`. Do not raise baselines to silence failures - split code.

## Writing standard (ASD-STE100)

All agent-authored prose (reports, findings, docs, prompts, agent messages, commit messages, code comments) must use ASD-STE100 Simplified Technical English. Rules: `.mivia/rules/90-writing-standard-ste100.md`.

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

Start every `feature-delivery` run with this script:

```bash
scripts/run-delivery-workflow.sh <label> <<'TASK'
...task text, any length, any number of lines...
TASK
```

The script sets `--allow-publish` and starts the run in the background. It
prints the log path, so you can start several runs and watch them together.

Do not call `mivia workflow run feature-delivery` directly. Without
`--allow-publish` the run does all the work, reaches its success terminal, then
stops at `delivery_pending` and opens no pull request.

### Live e2e test workflows (`e2e-split-test`, `e2e-pr-metadata-test`, `e2e-scope-escape-test`)

`.mivia/workflows/e2e-split-test.toml`, `.mivia/workflows/e2e-pr-metadata-test.toml`,
and `.mivia/workflows/e2e-scope-escape-test.toml`
(plus `.mivia/agents/e2e-engineer.toml` and `.mivia/workflows/templates/e2e-*.md`)
are real, checked-in workflows that exercise the delivery engine's repair
paths against the ACTUAL `MiviaLabs/mivia-agent` GitHub repo: real branches
pushed, real draft PRs opened, real `gh` and DeepSeek API calls.

- `e2e-split-test`: the diff-size gate and automatic split
  (`[stacking] split_deferred = true`) - its repair template deliberately
  never shrinks the diff, so the host's own split (and, when the run isn't
  part of a multi-chunk stack, delivery.EnsureFollowUpPublished) must do
  all the work.
- `e2e-pr-metadata-test`: the commit-subject repair path - implement
  deliberately emits an invalid `pr_title` on its first attempt, proving
  ValidateCommitSubject's rejection routes to repair and the agent's fix
  (reading the hint) succeeds on retry.
- `e2e-scope-escape-test`: the chunk-scope guard repair path - run in chunk
  mode with an explicit `chunk_plan` input, implement deliberately writes one
  file outside the declared slice, proving guardChunkScope's refusal routes
  to repair and the agent's fix (deleting the file per the hint) succeeds on
  retry.

**Never run either without the user explicitly asking for it in that
session.** Neither is part of `make verify`, CI, or any other automated
path, and that must stay true. Each workflow's `description` field repeats
this warning.

When the user does ask for a live delivery-engine smoke test:

```bash
./mivia workflow run e2e-split-test --input task="short description" --allow-publish
./mivia workflow run e2e-pr-metadata-test --input task="short description" --allow-publish
./mivia workflow run e2e-scope-escape-test --input task="short description" \
  --input stack_mode=chunk --input chunk=c1 --input pr_base=master --input stack_part=1/1 \
  --input chunk_plan='{"id":"c1","title":"scope smoke","files":["testdata/e2e-smoke/scope-ok.md"]}' \
  --allow-publish
mivia stack drive e2e-split-test   # only if decompose produced a multi-chunk plan
```

Keep the `task` input short (the rendered PR title/commit subject must pass
this repo's own `.mivia/policy/commit-message.json`, ≤72 chars, `type(scope):
subject` shape). After the run settles, close and delete-branch any PR it
opened - the workflow's own PR body already says "Safe to close/delete."
Never merge one.

### e2e suite runner (`scripts/e2e_suite.py`)

`scripts/e2e_suite.py` is a small, versioned suite over live e2e scenarios,
so a live delivery-engine check does not mean inventing a fresh ad hoc task
prompt every time. Same never-run-without-explicit-ask rule as above; it
never runs itself and is not part of `make verify`/CI. Three scenario
kinds: **topology** (drives the real `feature-delivery` workflow with a
task engineered to force a known chunk-dependency shape - independent
chunks, a DAG diamond, a wide fan-in, a linear chain, a single-package
run), **scripted** (the checked-in `e2e-*.toml` workflows above), and
**bug-fix** (a real `bug-fix.toml` run, scope narrowed to a bug-dense area,
told to fix only the first confirmed bug rather than hunt exhaustively -
small and bounded, not an open-ended audit).

```bash
scripts/e2e_suite.py list                 # see every scenario
scripts/e2e_suite.py run independent-3    # launch one, backgrounded
scripts/e2e_suite.py run --all            # launch the whole suite in parallel
scripts/e2e_suite.py status               # summarize every launched run
scripts/e2e_suite.py kill --all           # stop every launched driver process
```

Logs land in `.mivia/run-logs/e2e-suite/`, one file per scenario name, with
a `manifest.json` tracking pid/log/start time so `status`/`kill` work in a
later session too. As with the checked-in workflows above: close and
delete-branch any PR a run opens; never merge one.

## Layout

```text
cmd/mivia/           CLI entrypoint -> binary mivia
internal/            Go packages
.mivia/                 Canonical agent control surface
.mivia/hooks/        This repo's own mivia lifecycle hook scripts (project-scoped)
docs/                Human docs (OWNERS enforced)
scripts/             Guards, hooks, scans, contract tests
semgrep/             Agent-standards static rules
.githooks/           core.hooksPath entrypoints
```

## Skills (use when relevant)

| Skill | Role |
|-------|------|
| `verify-code-change` | Evidence ladder after code/config changes |
| `bug-audit` | Adversarial confirmed-bug hunt only (no false positives) |

Repo-native:

| Skill | Role |
|-------|------|
| `verify-change` | Mechanical gates + `mivia-report/v1` for scoped changes |
| `docs-update` | OWNERS-safe documentation edits |
| `secure-change` | Auth/secrets/network/tooling review |
| `simplification-review` | Landed-code over-engineering and pattern-fitness review |
| `performance-review` | Measurement-driven profiling and benchmark review |
| `concurrency-review` | Fan-out, pools, cancel, race |
| `architecture-review` | Boundaries, abstraction level, over-engineering (ADLC Step 0) |
| `feature-delivery` | Bounded feature slice delivery |

Workflow panel (read-only, JSON-only; used by the `feature-delivery` `review_panel` members):

| Skill | Role |
|-------|------|
| `panel-bug-audit` | Correctness member: reachable bugs, concurrency, persistence, reliability |
| `panel-secure-change` | Security member: authz, secrets, injection, SSRF, prompt injection |
| `panel-architecture-review` | Integration member: boundary fitness, dependency direction, abstraction cost |

## Workflows

When the workspace has `.mivia/workflows/`, the root agent has the workflow
tools by default. `workflow_run` admits and starts a named workflow.
`workflow_status`, `workflow_events`, `workflow_inspect`, and
`workflow_list_runs` observe runs. `workflow_deliver` publishes a
delivery-pending run, but only with `allow_publish=true`. `workflow_cancel`
stops a run. Use the workflow engine when a task fits an existing workflow
definition.

## Doctrines

- `.mivia/doctrines/engineering-working-contract.md` - standing engineering contract
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
