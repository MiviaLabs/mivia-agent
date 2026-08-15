# .mivia Control Surface

Product: **mivia** (MiviaLabs)
Binary: `mivia` (`cmd/mivia/`)
`.mivia/` is the canonical project-level control surface for agentic development in this repo. Root `AGENTS.md` is the canonical instruction file; `.mivia/` holds durable rules, skills, policy, and quality contracts that tool adapters reference.

## Read Order

1. `AGENTS.md`
2. `.mivia/INDEX.md` (this file)
3. **`.mivia/rules/05-adlc-agentic-development-lifecycle.md` - MANDATORY process. Read this before any work.**
4. Relevant other `.mivia/rules/*.md` in numeric order when multiple apply
5. Relevant `.mivia/doctrines/*.md`
6. Relevant `.mivia/skills/*/SKILL.md`
7. Relevant `.mivia/policy/*.json` when hooks, commits, or docs ownership are in play
8. Tool adapter files only when running that tool: `CLAUDE.md`, `.agents/`, `.claude/`, `.codex/`, `.github/copilot-instructions.md`

If an adapter conflicts with `AGENTS.md` or `.mivia/`, follow `AGENTS.md` / `.mivia/` and fix the adapter.

## Rules

### ⚠️ MANDATORY - read and follow before any work

`.mivia/rules/05-adlc-agentic-development-lifecycle.md` - **ADLC protocol: 7-step engineering cycle for all work. Do not skip.**
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
| `.mivia/rules/60-tools-project-language-generic.md` | Generic model-facing tools, default prompts, and portable review skill |
| `.mivia/rules/70-long-running-heartbeat.md` | Heartbeat protocol for long-running tasks |
| `.mivia/rules/80-commit-message.md` | Conventional commit format |

## Plans

Implementation plans and progress reports do not live in this repository. They
are prose about work in flight, not a user-facing manual and not an instruction
an agent reads, so they are kept in the sibling `mivia-agent-plans` repository.

Every `.md` file here must be one of two things: documentation a user reads, or
an instruction an agent follows. A plan is neither once the work ships; the
durable truth belongs in the OWNERS-registered canonical doc, in an invariant,
or in the code.

## Doctrines

- `.mivia/doctrines/engineering-working-contract.md` - standing engineering contract
- `.mivia/doctrines/evidence-before-claims.md` - from mivia-agent-skills
- `.mivia/doctrines/verification-is-part-of-delivery.md` - from mivia-agent-skills

## Skills

Canonical project skills (under `.mivia/skills/` only; do not fork into tool adapters):

Ported from **mivia-agent-skills** (higher reliability than agentkit MVP copies):

- `verify-code-change` - blast-radius verification ladder; PASS/PARTIAL/FAIL
- `bug-audit` - confirmed reachable bugs only; hard anti-false-positive rules

Repo-native:

- `verify-change` - mechanical package/gates report via `mivia-report/v1`
- `docs-update` - OWNERS-safe documentation edits; no duplicates
- `secure-change` - secrets, authz, network, tool isolation
- `concurrency-review` - subagent caps, pools, cancel, race
- `architecture-review` - portable structural review of boundaries, dependencies, abstraction cost, and evolution risk; runs at ADLC Step 0
- `simplification-review` - post-implementation over-engineering and pattern-fitness review of landed code
- `performance-review` - measurement-driven profiling and benchmarking; no findings without measurements
- `feature-delivery` - bounded feature slice with verification
- `workflow-runs-analysis` - read-only validated analysis of workflow-run ledger; process-quality findings (default window last 24h)
- `memory-housekeeping` - audit the memory store: verify facts, delete stale or duplicate entries, update outdated ones, create missing ones

Workflow panel (read-only, JSON-only; used by the `feature-delivery` and `bug-fix` `review_panel` members):

- `panel-bug-audit` - correctness member: reachable bugs, concurrency, persistence, reliability
- `panel-secure-change` - security member: authz, secrets, injection, SSRF, prompt injection, fail-closed defaults
- `panel-architecture-review` - integration member: boundary fitness, dependency direction, abstraction cost

`bug-audit`, `architecture-review`, `simplification-review`, and `performance-review` remain report-only. They do not commit or push.

## Policy

Machine-readable hook and agent policy:

| File | Purpose |
|------|---------|
| `.mivia/policy/commit-message.json` | Conventional commits: types, scopes, subject length |
| `.mivia/policy/agent-hook-bypass.json` | Blocked verification-bypass flags/env vars + corrective message |
| `.mivia/policy/docs-ownership.json` | Required `docs/OWNERS.yaml`, forbidden duplicate titles, canonical path rules |
| `.mivia/policy/pr-title.toml` | PR title and summary validation policy |

## Quality

- `.mivia/quality/contracts/` - project contract matrices for doctor/audit/runtime gates (populate as product surfaces land).
- `.mivia/quality/defect-taxonomy.md` - the recurring defect classes (`DC-1`..`DC-14`) derived from this repository's `fix` commit history, with a probe list per class and the chain-control sweep. Read the matching classes at ADLC Step 0 and Step 5. `verify-change` gates on it; `secure-change` cites `DC-10` and `DC-13`.

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
