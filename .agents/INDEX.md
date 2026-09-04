# .agents Control Surface

Product: **mivia** (MiviaLabs)
Binary: `mivia` (`cmd/mivia/`)
`.agents/` is the canonical project-level control surface for agentic development in this repo: durable rules, doctrines, skills, quality docs, and templates that tool adapters reference. Root `AGENTS.md` is the canonical instruction file. `.mivia/` is scoped to the product's own runtime config and state (`mivia.toml`, `workflows/`, `hooks/`, `agents/*.toml`, `policy/*.json` consumed by compiled Go code) - not agent instructions. `.mivia/` holds no skills: the `mivia` binary loads workspace skills from `.agents/skills/`.

## Read Order

1. `AGENTS.md`
2. `.agents/INDEX.md` (this file)
3. Relevant `.agents/rules/*.md` in numeric order when multiple apply -
   `05-adlc-agentic-development-lifecycle.md` governs substantial feature
   work, bug fixes, refactors, and cross-package changes, reached via the
   `delivery` skill
4. Relevant `.agents/doctrines/*.md`
5. Relevant `.agents/skills/*/SKILL.md`
6. Relevant `.mivia/policy/*.json` when hooks, commits, or docs ownership are in play
7. Tool adapter files only when running that tool: `CLAUDE.md`, `.claude/`, `.codex/`, `.github/copilot-instructions.md`

If an adapter conflicts with `AGENTS.md` or `.agents/`, follow `AGENTS.md` / `.agents/` and fix the adapter.

## Rules

### Delivery lifecycle (substantial work)

`.agents/rules/05-adlc-agentic-development-lifecycle.md` - **ADLC protocol: 7-step engineering cycle, reached via the `delivery` skill for substantial feature work, bug fixes, refactors, and cross-package changes.** Small, well-understood changes do not need it.
See also "Delivery process" in `AGENTS.md`.

### Reference rules (read when relevant)

| File | Purpose |
|------|---------|
| `.agents/rules/00-operating-doctrine.md` | Scope control, docs-first work, idempotency, verification contracts |
| `.agents/rules/01-output-budget.md` | Terse status, final-answer shape, task slicing |
| `.agents/rules/10-security-privacy.md` | Secrets, network, hooks, PII, YOLO mode, fail-closed protected actions |
| `.agents/rules/20-agent-quality.md` | Tests, mutation proofs, review gates, contract coverage |
| `.agents/rules/30-go-standards.md` | Go layout for `cmd/mivia` + `internal/`, errors, naming, embed |
| `.agents/rules/40-docs-ownership.md` | Single source of truth per topic; no parallel docs; `docs/OWNERS.yaml` |
| `.agents/rules/50-concurrency-subagents.md` | Subagents as tasks/goroutines; shared MCP; caps; no process farm |
| `.agents/rules/60-tools-project-language-generic.md` | Generic model-facing tools, default prompts, and portable review skill |
| `.agents/rules/70-long-running-heartbeat.md` | Heartbeat protocol for long-running tasks |
| `.agents/rules/80-commit-message.md` | Conventional commit format |

## Plans

Implementation plans and progress reports do not live in this repository. They
are prose about work in flight, not a user-facing manual and not an instruction
an agent reads, so they are kept in the sibling `mivia-agent-plans` repository.

Every `.md` file here must be one of two things: documentation a user reads, or
an instruction an agent follows. A plan is neither once the work ships; the
durable truth belongs in the OWNERS-registered canonical doc, in an invariant,
or in the code.

## Doctrines

- `.agents/doctrines/engineering-working-contract.md` - standing engineering contract
- `.agents/doctrines/evidence-before-claims.md` - from mivia-agent-skills
- `.agents/doctrines/verification-is-part-of-delivery.md` - from mivia-agent-skills

## Skills

Canonical project skills live under `.agents/skills/` as real directories.
The compiled `mivia` binary's loader (`internal/workspace.SkillsDir`,
returns `<root>/.agents/skills`) reads from this path; the loader's
`os.Root` sandbox cannot follow symlinks, so a skill must be a real
directory at this path to be discovered. Claude Code discovers skills
only under `.claude/skills/`, so `.claude/skills/<name>/SKILL.md` is an alias stub: it
repeats the canonical frontmatter block byte for byte and its body
points at `.agents/skills/<name>/SKILL.md`. The stub is a plain file,
not a symlink, because Git sets `core.symlinks=false` on Windows and a
cloned symlink turns into a plain text file. To add a skill: create a
directory under `.agents/skills/<name>/SKILL.md` with the YAML
frontmatter schema documented in any existing skill, then add the alias
stub under `.claude/skills/<name>/SKILL.md`. Keep bundled resources
beside the canonical file only. `scripts/verify_agent_config.py`
enforces this.

Ported from **mivia-agent-skills** (higher reliability than agentkit MVP copies):

- `verify-code-change` - blast-radius verification ladder; PASS/PARTIAL/FAIL
- `bug-audit` - confirmed reachable bugs only; hard anti-false-positive rules

Repo-native:

- `verify-change` - mechanical package/gates report via `mivia-report/v1`
- `docs-update` - OWNERS-safe documentation edits; no duplicates
- `docs-maintenance` - maintain documentation consistency, check ownership, and repair stale docs
- `secure-change` - secrets, authz, network, tool isolation
- `concurrency-review` - subagent caps, pools, cancel, race
- `architecture-review` - portable structural review of boundaries, dependencies, abstraction cost, and evolution risk; runs at ADLC Step 0
- `simplification-review` - post-implementation over-engineering and pattern-fitness review of landed code
- `performance-review` - measurement-driven profiling and benchmarking; no findings without measurements
- `feature-delivery` - bounded feature slice with verification
- `workflow-feature-delivery` - deliver one scoped feature in a workflow worktree with verification and report
- `review` - meta-skill that routes a diff to the right per-lens skill (no duplicated logic)
- `review-synthesis` - synthesize multiple review reports into one structured result
- `delivery` - ADLC loop in skill form; points at the rule, role files, and runtime templates without duplicating them
- `fast-bug-audit` - fast opportunistic read-only hunt for reachable confirmed bugs
- `logic-review` - line-level correctness review inside one function and its tests
- `test-review` - audit package tests for truth, coverage quality, and assertion rigor
- `workflow-runs-analysis` - read-only validated analysis of workflow-run ledger; process-quality findings (default window last 24h)
- `session-analysis` - read-only validated analysis of chat sessions in the durable chat ledger; metadata-only (no message content); default window last 24h; owned by the unrestricted root
- `capture` - record a durable decision or correction to `.agents/memories/`
- `memories-housekeeping` - audit `.agents/memories/` for staleness, duplicates, and orphan facts

Workflow panel (read-only, JSON-only; used by the `feature-delivery` and `bug-fix` `review_panel` members):

- `panel-bug-audit` - correctness member: reachable bugs, concurrency, persistence, reliability
- `panel-secure-change` - security member: authz, secrets, injection, SSRF, prompt injection, fail-closed defaults
- `panel-architecture-review` - integration member: boundary fitness, dependency direction, abstraction cost

`bug-audit`, `fast-bug-audit`, `architecture-review`, `simplification-review`, and `performance-review` remain report-only. They do not commit or push.

## Subagents

Markdown subagent role definitions live under `.agents/agents/` for the
human and ADLC-driven workflow. The four standard roles are:

| Role | File | Tools |
|------|------|-------|
| `planner` | `.agents/agents/planner.md` | read-only |
| `plan-reviewer` | `.agents/agents/plan-reviewer.md` | read-only |
| `builder` | `.agents/agents/builder.md` | read + write + run_command |
| `reviewer` | `.agents/agents/reviewer.md` | read-only |

Frontmatter schema and the loading contract are documented in
[`.agents/agents/README.md`](agents/README.md). The `mivia` binary and workflow
engine load workspace agents directly from `.agents/agents/*.md`. Run `make
agents-check` after editing any role file - the script enforces the frontmatter
schema, the filename / `name` match, and the role-specific tool and disallowed-operations constraints.

## Policy

Machine-readable hook and agent policy:

| File | Purpose |
|------|---------|
| `.mivia/policy/commit-message.json` | Conventional commits: types, scopes, subject length |
| `.mivia/policy/agent-hook-bypass.json` | Blocked verification-bypass flags/env vars + corrective message |
| `.mivia/policy/docs-ownership.json` | Required `docs/OWNERS.yaml`, forbidden duplicate titles, canonical path rules |
| `.mivia/policy/pr-title.toml` | PR title and summary validation policy |
| `.mivia/policy/destructive-commands.json` | Destructive command patterns blocked without confirmation |
| `.mivia/policy/diff-coverage.json` | Diff test coverage thresholds and path exclusions |
| `.mivia/policy/go-structure.json` | Go file and function LOC limits and file size baselines |
| `.mivia/policy/import-layers.json` | Allowed Go package import dependencies and layer constraints |
| `.mivia/policy/required-paths.json` | Mandatory repository directories and configuration paths |
| `.mivia/policy/invariant-skips.json` | Allowlisted OS-capability skips in invariant tests |
| `.mivia/policy/test-skips.json` | Allowlisted test skips and unreviewed-skip prevention |
| `.mivia/policy/wire-vocabulary.json` | Files that restate the `mivia.chat.v1` event vocabulary and the contract they must equal |
| `.mivia/policy/mutation/*.json` | Per-package mutation kill-rate floors and audited denylists |

## Quality

- `.agents/quality/contracts/` - project contract matrices for doctor/audit/runtime gates (populate as product surfaces land).
- `.agents/quality/defect-taxonomy.md` - the recurring defect classes (`DC-1`..`DC-40`) derived from this repository's `fix` commit history, with a probe list per class and the chain-control sweep. Read the matching classes at ADLC Step 0 and Step 5. `verify-change` gates on it; `secure-change` cites `DC-10` and `DC-13`.

## Runtime Artifacts

- `.mivia/runs/` is for workflow traces and summaries and must be gitignored.
- Never persist raw prompts, raw model outputs, provider payloads, credentials, or plausible secrets under `.mivia/runs/` or elsewhere in the tree.

## Documentation Ownership

- Topic ownership and canonical paths are declared in `docs/OWNERS.yaml`.
- Agents update the existing canonical document for a topic; they do not create parallel or duplicate docs (see `.agents/rules/40-docs-ownership.md` and `.mivia/policy/docs-ownership.json`).

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

After changing `AGENTS.md`, `.agents/`, `.mivia/`, adapter configs, hooks, or Semgrep agent standards:

1. Re-read this INDEX and the touched rule/policy.
2. Run `make verify` (or the narrowest contract test for the change).
3. Report what was verified and what remains unverified.
