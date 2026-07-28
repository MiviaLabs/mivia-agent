# Bug Audit: ADLC Files — Fresh Eyes Round

**Auditor**: Fresh-eyes cross-file audit
**Date**: 2025-07-17
**Scope**: Host ADLC vs shipped ADLC, INDEX files, AGENTS.md, commit-message rules, cross-references

---

## Finding 1 — No sync mechanism between host and shipped ADLC

**Severity**: HIGH
**Files**: `.ai/rules/05-adlc-agentic-development-lifecycle.md` vs `ship/rules/05-adlc-agentic-development-lifecycle.md`
**Status**: **confirmed**

The two files are **structurally different documents** with wildly different lengths and detail:

| Aspect | Host ADLC | Shipped ADLC |
|--------|-----------|--------------|
| Lines | ~530 lines | ~95 lines |
| Templates | 10 templates (Plan, Task, Task List, Validation Report, Bug Audit Report, Disposition Log, Handoff, Error/BLOCKED, Reviewer Output) | None |
| Tool Reference table | 7-row table with detailed usage + notes + decision tree | 3-row simplified table |
| File Conflict & Ownership rules | Entire section | Missing |
| Invariant Enforcement | Entire section referencing `internal/*` | Missing |
| Rejection & Rollback Rules | 16-row table | Missing |
| Escalation Protocol | Section for stuck sub-agents | Missing |
| Artifact Chain | Section listing all artifacts | Sections present but condensed |
| Step detail | Fully detailed with scorecard criteria, gate definitions, duration caps | Condensed to 1-3 bullet points per step |

There is **no mechanism** (script, test, or CI check) to keep them in sync. If a template is added or a step is modified in the host ADLC, the shipped ADLC must be manually updated. There is no cross-reference test, no diff gate, no shared source of truth.

**Recommendation**: Either:
- (a) Generate the shipped ADLC from the host ADLC via a script (strip host-specific sections + internal references), OR
- (b) Add a CI gate that alerts when they diverge beyond allowed differences, OR
- (c) Keep the shipped ADLC as a short "welcome" doc that points to the host ADLC for the full protocol.

---

## Finding 2 — `ship/AGENTS.md` references mivia source repo that users don't have

**Severity**: MEDIUM
**File**: `ship/AGENTS.md:2`
**Status**: **confirmed**

The file says:

> *"For host-specific instructions (developing mivia itself), see the `.ai/` directory in the mivia source repo."*

An end user who copied the `mivia` binary to their own project **does not have** the mivia source repo. This line will confuse users who followed the dead-end reference. They have no `.ai/` directory (that's what gets auto-written), and they certainly don't have the mivia source.

**Recommendation**: Remove this line from `ship/AGENTS.md`. The shipped edition should not mention host development at all. If absolutely needed, rephrase to: *"If you are developing the mivia agent itself, see the mivia source repository."*

---

## Finding 3 — `ship/INDEX.md` has broken references to non-existent shipped rules

**Severity**: HIGH
**File**: `ship/INDEX.md` (Reference rules table)
**Status**: **confirmed**

The `ship/INDEX.md` reference table lists these files, which **do not exist** in `ship/rules/`:

| Referenced in ship/INDEX.md | File exists on disk? |
|---|---|
| `.ai/rules/20-agent-quality.md` | ❌ No `ship/rules/20-agent-quality.md` |
| `.ai/rules/50-concurrency-subagents.md` | ❌ No `ship/rules/50-concurrency-subagents.md` |
| `.ai/rules/60-tools-project-language-generic.md` | ❌ No `ship/rules/60-tools-project-language-generic.md` |
| `.ai/rules/70-long-running-heartbeat.md` | ❌ No `ship/rules/70-long-running-heartbeat.md` |

Additionally, `ship/INDEX.md` references these doctrines:
| `.ai/doctrines/evidence-before-claims.md` | ❌ No `ship/doctrines/` directory at all |
| `.ai/doctrines/verification-is-part-of-delivery.md` | ❌ No `ship/doctrines/` directory at all |

The shipped INDEX is auto-written to a user's `.ai/` directory. When the agent reads it and tries to follow `.ai/rules/20-agent-quality.md`, the file won't exist. This creates a **broken reference** that the agent may attempt to follow, waste time searching, or hallucinate content.

**Files that DO exist on disk** in `ship/rules/`:
- `00-operating-doctrine.md` ✅
- `01-output-budget.md` ✅
- `05-adlc-agentic-development-lifecycle.md` ✅ (but see Finding 1)
- `10-security-privacy.md` ✅
- `80-commit-message.md` ✅

**Recommendation**: Either (a) create the missing `ship/rules/` files, or (b) remove the references from `ship/INDEX.md`.

---

## Finding 4 — Host ADLC references `internal/{cli,tools,agent,...}` — shipped ADLC correctly omits these

**Severity**: INFO (positive finding)
**File**: `.ai/rules/05-adlc-agentic-development-lifecycle.md` (Invariant Enforcement section)
**Status**: **confirmed — correct behavior**

The host ADLC Invariant Enforcement section references:
```
internal/cli/, internal/tools/, internal/agent/, internal/chat/,
internal/config/, internal/ledger/, internal/coordinator/,
internal/events/, internal/storage/
```

The shipped ADLC (`ship/rules/05-adlc-*`) does **not** contain this section. ✅ This is correct — shipped users don't have these internal packages.

---

## Finding 5 — `write_file()` in Step 0 — tool name used correctly

**Severity**: INFO (no issue)
**File**: `.ai/rules/05-adlc-agentic-development-lifecycle.md` (Step 0)
**Status**: **confirmed — correct**

Step 0 says:

```
write_file(.ai/plan/<name>/audit/.placeholder)
write_file(.ai/plan/<name>/evidence/.placeholder)
(write_file auto-creates parent directories.)
```

`write_file` IS one of the agent's available tools (documented in the agent's toolset). It's used correctly as a tool invocation, not a shell command. The comment correctly notes it auto-creates parent directories. No issue here.

The shipped ADLC does not include this detail, so the question about shipped context is moot.

---

## Finding 6 — Tool Reference tables are inconsistent between host and shipped

**Severity**: MEDIUM
**Files**: `.ai/rules/05-adlc-agentic-development-lifecycle.md` (Tool Reference section) vs `ship/rules/05-adlc-agentic-development-lifecycle.md` (Tool Reference section)
**Status**: **confirmed**

| Tool/Feature | Host ADLC | Shipped ADLC |
|---|---|---|
| `dispatch_tasks` for Step 0 (challenge) | ✅ Detailed | ✅ Mentioned |
| `dispatch_tasks` for Step 2 (validation) | ✅ Mentioned | ❌ Missing |
| `dispatch_tasks` for Step 5 (audit) | ✅ Detailed | ✅ Mentioned |
| `spawn_agent` for Step 4 (waves) | ✅ Detailed with `wait: "run"` | ✅ Mentioned |
| `delegate` for bug fixes | ✅ Documented | ❌ Missing |
| `inspect_agents` / `cancel_run` for stuck agents | ✅ Documented | ❌ Missing |
| Direct execution for Step 6 (build/test) | ✅ Documented | ❌ Missing |
| `partial_results: true` requirement | ✅ Documented | ✅ Mentioned |
| Tool Decision Tree | ✅ Yes | ❌ Missing |
| Handler type guidance (`multi_step` vs `oneshot`) | ✅ Detailed | ❌ Missing |

The shipped ADLC uses `dispatch_tasks` and `spawn_agent` but doesn't document `delegate`, `inspect_agents`, `cancel_run`, or direct execution. If a shipped user's agent gets stuck, the shipped ADLC offers no guidance on how to recover.

**Recommendation**: Either add the missing tools to the shipped ADLC Tool Reference, or note that these are mivia-specific tools available to the agent.

---

## Finding 7 — Shipped ADLC Step 0 says "Dispatch 2-4 hostile challenge agents" — potentially inappropriate for generic projects

**Severity**: MEDIUM
**File**: `ship/rules/05-adlc-agentic-development-lifecycle.md` (Step 0)
**Status**: **confirmed**

The shipped ADLC Step 0 says:

> *Dispatch 2-4 hostile challenge agents to attack the plan.*

And Step 5 says:

> *Dispatch hostile auditors. Loop until zero bugs or 5 rounds max.*

For a generic user project (e.g., a Python web app, a Rust CLI tool), dispatching 2-4 hostile challenge agents that read, critique, and potentially modify files is **aggressive default behavior**. The user may not:
- Know what "hostile challenge agents" means
- Want their agent spending API credits on adversarial audits for every change
- Have the context window budget for 4 parallel auditors
- Want automated audit loops that could consume significant time and cost

The host ADLC has this because it's developing a multi-agent system where adversarial testing is essential. But generic projects (documentation updates, config changes, small fixes) don't need this overhead.

**Recommendation**: Either (a) add a note that hostile audit is optional and can be skipped for trivial/confidence changes, (b) reduce to 1 auditor by default for generic projects, or (c) make it a configurable setting.

---

## Finding 8 — Shipped `80-commit-message.md` has different types/scopes than host versions

**Severity**: MEDIUM
**Files**: `ship/rules/80-commit-message.md` vs `.ai/rules/80-commit-message.md` vs `AGENTS.md`
**Status**: **confirmed**

The shipped `80-commit-message.md` lists allowed types as:
```
feat, fix, docs, style, refactor, test, chore
```
(with no scopes — scope is "optional but recommended")

The host `.ai/rules/80-commit-message.md` lists:
```
feat, fix, docs, chore, test, refactor, build, ci, perf, style, revert, security
```
AND has required scopes (`cli, agent, mcp, hooks, ai, docs, security, quality, build, ci, test, deps, release`).

The host `AGENTS.md` lists yet another set of scopes with different usage descriptions.

This means:
1. The shipped version is more generic (correct for end users), but it conflicts with the host version
2. An end user who gets the shipped `80-commit-message.md` will have a looser commit policy than the mivia dev team
3. The host has `security` as a type, the shipped does not
4. The host requires scope, the shipped makes scope optional

This divergence is intentional (different audiences), but **the shipped version also omits `build`, `ci`, `perf`, `revert`, `security` types** compared to the host. If these rules are used to generate the shipped version, the types should be a superset, not a subset.

---

## Finding 9 — Host ADLC references `make invariants` and `make validate-invariants` — no equivalent in shipped ADLC

**Severity**: LOW
**File**: `.ai/rules/05-adlc-agentic-development-lifecycle.md` (Invariant Enforcement + Test Types table)
**Status**: **confirmed**

The host ADLC Test Types table has an "Invariant test" row that says:

> *Gate: `make invariants` passes*

And the Invariant Enforcement section says to run invariant tests. The shipped ADLC has no mention of invariants. While invariants are host-specific (Go make targets), the shipped ADLC should at minimum note that the agent should look for project-specific invariants (e.g., `make check`, `npm test`, `cargo check`) rather than silently skipping this verification step.

---

## Finding 10 — Host ADLC has "File Conflict & Ownership Rules" section — shipped ADLC omits it

**Severity**: LOW
**File**: `.ai/rules/05-adlc-agentic-development-lifecycle.md` (File Conflict section) vs missing from `ship/rules/05-adlc-agentic-development-lifecycle.md`
**Status**: **confirmed**

The host ADLC has an entire section on file conflict rules ("Two tasks in different waves must not touch the same file"). The shipped ADLC has no equivalent. In a generic project with multiple waves of parallel tasks, this absence could lead to merge conflicts that the agent doesn't know how to avoid.

---

## Summary Table

| # | Finding | Severity | File | Line |
|---|---------|----------|------|------|
| 1 | No sync mechanism between host ADLC and shipped ADLC (structurally different documents) | HIGH | `.ai/rules/05-adlc-*` vs `ship/rules/05-adlc-*` | Entire files |
| 2 | `ship/AGENTS.md` references mivia source repo that end users don't have | MEDIUM | `ship/AGENTS.md` | 2 |
| 3 | `ship/INDEX.md` references 4 rules + 2 doctrines that don't exist in `ship/` | HIGH | `ship/INDEX.md` | Reference table |
| 4 | Host ADLC references `internal/*` packages — shipped correctly omits them | INFO (✅) | `.ai/rules/05-adlc-*` vs `ship/rules/05-adlc-*` | Invariant Enforcement |
| 5 | `write_file()` is a valid tool name, used correctly in Step 0 | INFO (✅) | `.ai/rules/05-adlc-*` | Step 0 |
| 6 | Tool Reference tables inconsistent — shipped missing `delegate`, `inspect_agents`, `cancel_run`, direct execution | MEDIUM | `.ai/rules/05-adlc-*` vs `ship/rules/05-adlc-*` | Tool Reference |
| 7 | Shipped ADLC demands "hostile challenge agents" by default — inappropriate for generic projects | MEDIUM | `ship/rules/05-adlc-*` | Step 0, Step 5 |
| 8 | Shipped `80-commit-message.md` has different types than host version | MEDIUM | `ship/rules/80-commit-message.md` vs `.ai/rules/80-commit-message.md` | Types list |
| 9 | Invariant verification (make invariants) missing from shipped ADLC | LOW | `ship/rules/05-adlc-*` | (missing section) |
| 10 | File conflict/ownership rules missing from shipped ADLC | LOW | `ship/rules/05-adlc-*` | (missing section) |

---

## Cross-cutting Concern

The host and shipped ADLC documents have **diverged into completely different documents** with different structure, detail level, and completeness. The host ADLC is a ~530-line detailed specification with templates, tables, and protocols. The shipped ADLC is a ~95-line summary. There is no shared source, no generation pipeline, and no diff test.

**If you fix a bug in one, you must fix it in both — and there is no mechanism to remember or enforce this.**

The most impactful fix would be Finding 1 (sync mechanism) and Finding 3 (broken references in shipped INDEX). Without those, the shipped experience is unreliable.

