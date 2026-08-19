# ADLC - Agentic Development Lifecycle

**⚠️ THIS IS THE MANDATORY PROCESS FOR ALL WORK IN THIS REPO.**
Read this file before starting any task. See also `AGENTS.md` ("Mandatory process" section) and `.agents/INDEX.md` ("MANDATORY" section).

**Scope**: All feature work, bug fixes, refactors, and cross-package changes.
**Override**: This rule governs *how* work is sequenced and verified.

**Storage model**: Zero files. Everything lives in the orchestrator's context (ephemeral) or in the LedgerRepository (SQLite/memory via `spawn_agent`). No `.md` files are written for workflow artifacts.

**Fast Path**: Trivial changes (≤5 lines, single file, no new types) may skip Steps 0-3 and go directly to Step 4.

---

## Principles

1. **TDD - tests before code.** RED (failing assertion) → GREEN (passing code). Always.
2. **Micro-tasks for agents.** 1 function OR 1 file per task. Fresh context per task.
3. **Challenge before build.** Every plan is attacked before any code is written.
4. **Test-drive the bug audit.** When uncertain about a bug report, write a test first.
5. **Fail fast, roll back.** If a step reveals a plan flaw, return to Step 0.
6. **Idempotency.** Every step must be safely re-runnable.
7. **Zero files for workflow.** No `.md` plan files. No artifact directories. Everything stays in the orchestrator's context or tool results.

---

## Test Types

| Type | When | Who | Scope | Gate |
|------|------|-----|-------|------|
| **Unit test (RED)** | Step 4a, per micro-task | Sub-agent | Single function. Must compile and fail assertion. | `go test -run TestXxx ./pkg/...` assertion failure |
| **Unit test (GREEN)** | Step 4b | Sub-agent | Production code passing the RED test. | `go test -run TestXxx ./pkg/...` pass |
| **Integration test** | Step 4c | Dedicated sub-agent | End-to-end across layers. | `go test -run TestIntegration ./...` pass |
| **Race test** | After every wave + Step 6 | Orchestrator | Detects data races. | `go test -race ./affected...` pass |

---

## Tool Reference

Every ADLC step maps to specific built-in tools. Do not use `write_file`, `mkdir`, or shell commands for workflow state.

| ADLC Step | Tool | Usage |
|-----------|------|-------|
| **Step 0** - Challenge plan | `dispatch_tasks` | 2-4 parallel hostile reviews: `agent: "reviewer"` + `skill: "architecture-review"` for structure, `agent: "auditor"` for correctness. Route each task by `agent` (+ optional `skill`); no `handler` field |
| **Step 2** - Validate tasks | `dispatch_tasks` | 1 validator per wave: `agent: "verifier"` + `skill: "verify-code-change"`. Route each task by `agent` (+ optional `skill`); no `handler` field |
| **Step 4** - Implement | `spawn_agent` (waves with deps) / `dispatch_tasks` (parallel within wave) | `agent: "go-engineer"`, `wait: "run"` for sequential waves |
| **Step 4** - Sub-agent stuck | `inspect_agents` → `cancel_run` | Check status, abort if >2min stuck |
| **Step 5** - Bug audit | `dispatch_tasks` | 3-4 auditors: `agent: "auditor"` + `skill: "bug-audit"`. Add one `agent: "performance"` + `skill: "performance-review"` when the change touches a hot path. Route each task by `agent` (+ optional `skill`); no `handler` field |
| **Step 5** - Fix bug | `delegate` | Single focused fix: `multi_step: true`, `timeout_seconds: 60` |
| **Step 6** - Verify | Direct execution, or `dispatch_tasks` with `agent: "verifier"` + `skill: "verify-change"` | `go build ./... && go vet ./... && go test -race ./...` |

### Decision Tree

```
Need parallel independent work?     → dispatch_tasks
Need sequential waves with deps?    → spawn_agent + join_run
Need a single focused task?         → delegate
Need to check status of work?       → inspect_agents
Need to cancel stuck work?          → cancel_run
Need to run build/test commands?    → Direct execution (not a tool)
```

### Handler Types - Critical

`dispatch_tasks` and `spawn_agent` tasks have **no `handler` field** - do not pass one. The strict task schema rejects unknown fields, so a stray `handler: "multi_step"` on a `dispatch_tasks` task fails the **whole** batch with `json: unknown field "handler"`. The routed agent definition - selected by `agent` plus an optional `skill` - determines the loop and tools: every defined agent/skill runs a multi-step loop with tool access scoped to its definition.

`delegate` is the only tool with a `multi_step` boolean. Set `multi_step: true` on `delegate` for a single focused sub-agent with full tool access; the default (false) is one LLM call with no tools.

**One agent timing out or hanging never costs you the others.** Every task reports
its own result and status, so a challenge or audit round returns what the surviving
agents found regardless.

Do not pass `partial_results`. The flag was removed because it had no observable
effect, and `dispatch_tasks` rejects unknown parameters. A rejected call fails the
whole batch and is reported as a bare `{"status":"failed"}`.

---

## Protocol (7 Steps - no file operations)

All artifacts are ephemeral - held in the orchestrator's context or passed as sub-agent results. No files are written for plans, tasks, evidence, or audit logs.

### Step 0 - Plan, Challenge & Lock

**Who**: Orchestrator (you).
**Duration cap**: 20 minutes.

**Actions**:

1. **Read the codebase.** Read every relevant file - interfaces, implementations, callers, tests, config wiring. If touching sensitive packages, also read `.mivia/invariants.md` and run invariant tests.

2. **Build the plan in context.** The plan is NOT a file. It's a mental model you hold. It must cover:
   - Goal (one sentence)
   - Files to create (exact paths)
   - Files to modify (exact paths + changes)
   - API surface (exact Go signatures)
   - Dependency graph (Wave 1 → Wave 2 → …)
   - Test strategy (named test scenarios)
   - Plan scorecard (self-score PASS/FAIL against: compile, no cycles, no breaking API, testable in isolation, backward-compatible config, every function has a test)
   - Rollback criterion (what kills this plan)

3. **Dispatch 2-4 parallel challenge agents via `dispatch_tasks`.** One is
   `agent: "reviewer"` applying skill `architecture-review` (structure: boundaries,
   dependency direction, abstraction level, speculative generality); the others are
   `agent: "auditor"` attacking correctness. Give the panel diverse lenses - a
   structural finding and a correctness finding are worth more together than two of
   either.
   ```
   dispatch_tasks({
     tasks: [
       {id:"c1", agent:"reviewer", skill:"architecture-review", prompt:"Hostile structural review of plan: ...", timeout_seconds: 120},
       {id:"c2", agent:"auditor", prompt:"Hostile correctness review of plan: ...", timeout_seconds: 120}
     ]
   })
   ```
   Each agent receives the plan description in their prompt.

4. **Disposition every challenge output.** For each finding: confirmed → update plan in context. Rejected → note rationale. Save nothing to disk.

5. **Lock the plan.** The plan is now fixed in your context. Do not deviate during implementation. If a blocking discovery occurs mid-implementation, pause and return to Step 0.

**Gate**: All challenges dispositioned, structural and correctness alike. Scorecard all PASS.

---

### Step 1 - Micro-Task Breakdown

**Who**: Orchestrator.
**Duration cap**: 10 minutes.

**Actions**:

1. Slice the locked plan into micro-tasks. Rules:
   - **1 file per task.** A task creates OR modifies one file, never both.
   - **1 function per production task.** If a file needs 3 functions, that's 3 tasks.
   - **Test task precedes each production task.** For every production task, a test task goes first (same wave).
   - **Reviewer every 2-3 implementation tasks.** Placed in the next wave - they read and validate.

2. Declare dependency waves in your context:
   ```
   Wave 1: [t1a (test), t1b (skeleton)]  - foundation
   Wave 2: [t2 (impl), t3 (review)]        - impl + review
   ```

3. Every task in your context must specify: ID, Wave, File, Type (test|prod|review), API, Depends on, Verification command, Timeout, Context scope (≤5 files).

**Gate**: No task exceeds 1 file. Every production task has a preceding test task. Every 2-3 production tasks has a reviewer in the next wave. Context scope ≤5 files.

---

### Step 2 - Validate Each Task

**Who**: Parallel sub-agents via `dispatch_tasks`.
**Duration cap**: 3 minutes per validator.

**Actions**:

1. Dispatch 1 validator per wave:
   ```
   dispatch_tasks({
     tasks: [{id:"v1", agent:"verifier", skill:"verify-code-change", prompt:"Validate tasks: [task specs]... read context scope files, is this implementable?", timeout_seconds: 60}]
   })
   ```

2. Each validator reads the actual Go files (from the context scope) and outputs PASS or REJECT with reasons. Results come back via tool output - no files written.

**Gate**: All PASS. Any REJECT → return to Step 1. 2nd REJECT on same task → Step 0.

---

### Step 3 - Verify & Finalize

**Who**: Orchestrator.
**Duration cap**: 5 minutes.

**Actions**:

1. Read all validation results from context.
2. Lock the task list in your context. No further changes without returning to Step 0.

**Gate**: Task list immutable in context.

---

### Step 4 - Orchestrate Implementation (TDD)

**Who**: Orchestrator + sub-agents via `spawn_agent` / `dispatch_tasks`.

**Per micro-task: RED → GREEN → handoff (all in context, no files)**

1. **RED phase** (test tasks only):
   - Write a test that compiles and FAILS an assertion on the target API.
   - Verification: `go test -run TestXxx ./pkg/...` → assertion failure (NOT compile error).
   - Save the test failure output in context (not to disk).
   - Do NOT write production code in a RED task.

2. **GREEN phase** (production tasks only):
   - Write MINIMAL production code that makes the RED test pass.
   - Verification: `go test -run TestXxx ./pkg/...` → PASS.

3. **Handoff in context**: After each task, pass the relevant info (files written, test status, deferred decisions) to the next sub-agent via the next task's prompt.

**Wave execution:**

1. Execute waves **in order** using `spawn_agent` with `wait: "run"`. Wave N never starts until Wave N-1 gates pass.
2. Within a wave, dispatch parallel tasks via `dispatch_tasks`.
3. **Reviewer tasks** in Wave N read Wave N-1 code via tool output. REJECT blocks the wave - orchestrator must fix before proceeding.
4. Sub-agents BLOCKED >2 min → inspect with `inspect_agents`, cancel with `cancel_run`.
5. **Wave gate:** `go build ./... && go test -race ./<affected>/...` must pass.
   - Quick fix (<5 lines) → apply directly, re-verify, proceed.
   - Plan flaw → return to Step 0.

**Gate**: All waves pass `go build` + `go test -race`. RED phase assertion failures logged in context. GREEN phase tests pass.

---

### Step 5 - Bug Audit Loop

**Who**: Orchestrator + 3-4 hostile sub-agents.
**Duration cap**: 3 rounds default, 5 max.

**Actions**:

1. Dispatch 3-4 hostile auditors via `dispatch_tasks`:
   ```
   dispatch_tasks({
     tasks: [{id:"a1", agent:"auditor", skill:"bug-audit", prompt:"Hostile audit of: changed files... find bugs", timeout_seconds: 120}]
   })
   ```
   When the change touches a hot path, persistence, or fan-out, add one
   `agent: "performance"` + `skill: "performance-review"` task to the panel.
   For quality (not defect) review of the landed diff, a `agent: "reviewer"`
   + `skill: "simplification-review"` task may join one round; its findings
   are dispositioned like challenge output, not treated as bugs.
   Result vocabulary: quality lenses report `Result: FINDINGS` - advisory,
   dispositioned by the orchestrator, never a gate. Only a `BLOCK` from a
   review skill or a confirmed bug from an auditor gates the round.

2. Per finding (handled in context - no files):
   - **Confirmed**: fix bug, re-run `go test -race ./...`, keep result in context.
   - **Rejected**: write a targeted test proving it's not a bug. Keep test in codebase.
   - **Uncertain**: write a targeted test. If passes → rejected. If fails → confirmed.

3. Loop until zero bugs. The round limit is configured via subagents.max_audit_rounds
   in mivia.toml (default: 0, meaning unlimited). If the same bug keeps
   reappearing after 3 fix attempts, escalate to Step 0 (plan rejected).

4. While auditors run, periodically call inspect_agents to check progress.
   If any audit agent is stuck >2 minutes, cancel_run it and dispatch a
   replacement. Never let stuck agents delay the loop.

**Gate**: All auditors report zero bugs. `go test -race ./...` passes on ALL packages.

**Unit/race-clean is not the same claim as "verified".** For a change in
`internal/workflows/delivery/` or `internal/workflows/controller/` (the
stacked-delivery engine): before telling the user the work is done, state
explicitly that only static review and unit/integration tests ran, and offer
a live e2e smoke run (`e2e-split-test`, `e2e-pr-metadata-test`,
`e2e-scope-escape-test`, or an ad hoc `scripts/run-delivery-workflow.sh`
task) as the next step. Never imply "tests pass" means "this works" for this
subsystem - across this codebase's history, live e2e against the real
GitHub repo is what caught nearly every defect in this class; unit tests
alone did not. AGENTS.md still governs: never run a live e2e workflow
without the user explicitly asking for it in that session - offer, don't run.
The runbook lives in `docs/development/agent-workflow.md`.

---

### Step 6 - Commit & Push

**Who**: Orchestrator.
**Duration cap**: 5 minutes.

**Actions**:

1. **Diff review**: `git diff --cached` - check for debug code, secrets, out-of-scope files, binaries.
2. **Final verification**: `go build ./... && go vet ./... && go test -race ./...`
3. **TDD audit**: verify every new production file has a corresponding `_test.go`. If missing, return to Step 4.
4. Conventional commit message (`type(scope): subject`, ≤72 chars).
5. Body: what changed, why, verification status.
6. `git push`.

**Gate**: Push succeeds. Tree clean. Diff review passed. Every production file has its test.

---

## Fast Path (trivial changes)

**Trivial** = ≤5 lines, single file, no new types, no config, no test file creation,
AND not in `internal/workflows/` (any subpackage).
A change under `internal/workflows/` is never Fast Path, regardless of line count:
their bugs are near-universally a missed sibling site (see Invariant
Enforcement above), which only Step 0's caller-reading and Step 5's
multi-auditor pass catch - the exact machinery Fast Path skips.
- Skip Steps 0-3. Implement directly in Step 4.
- Step 5: 1 hostile auditor (not 3-4).
- Step 6: normal commit.

---

## Rejection & Rollback Rules

| Condition | Action |
|-----------|--------|
| Step 0 challenge reveals fundamental design flaw | Plan rejected. Start over. |
| Step 0 structural finding: an abstraction nothing will reach and nothing has contracted | Cut it from the plan, or record the sequencing constraint that makes it reachable. Re-score. |
| Step 0 scorecard any FAIL | Plan rejected. |
| Step 2 validator REJECTs | Return to Step 1. 2nd REJECT on same task → Step 0. |
| Step 4 RED phase missing (test not written first) | Task rejected. Redo. |
| Step 4 RED test doesn't compile (just "undefined") | Task rejected. Write assertion-failing test. |
| Step 4 reviewer REJECTs | Orchestrator fixes. If fix >5 lines → return to Step 1. |
| Step 4 wave fails - plan flaw | Return to Step 0. |
| Step 5 audit loop exceeds configured max_audit_rounds | Plan rejected. Return to Step 0 with evidence. |
| Step 5 fix breaks existing tests | Halt. Revert. Re-analyse. |
| Step 6 missing test for production file | Return to Step 4. Do not commit. |
| Any regression discovered | Halt, revert, Step 0. |

---

## Invariant Enforcement

Before Step 0 and Step 4, read `.mivia/invariants.md` + run invariant tests if touching: `internal/cli/`, `internal/tools/`, `internal/agent/`, `internal/chat/`, `internal/config/`, `internal/ledger/`, `internal/coordinator/`, `internal/events/`, `internal/storage/`, `internal/workflows/` (the whole tree - not just `delivery/`/`controller/`: the recurring bug class also shipped in `localengine/`, `compiler/`, `ledger/`, and `definition/`).

If an invariant fails → blocked until restored or manifest updated.

`internal/workflows/` is on this list because it is a dual/multi-path system
by construction (CLI delivery vs. local-engine delivery in `localengine/`,
fresh admission vs. resume, per-chunk steps vs. their sibling steps in the
same pipeline, the compiler's synthesis vs. its resume-compile twin): a fix
at one path is not done until every structurally parallel path has been
checked. This class -
"satisfied the invariant at the one site the failure was observed at, shipped
the next bug at the sibling site" - is this codebase's most frequently
repeated root cause. See the bug-audit and panel-bug-audit skills' "one
invariant, several sites, one patched" probe; run it explicitly (Step 0 and
Step 5) for any change in these two packages.

---

## Escalation Protocol

Sub-agent BLOCKED >2 min:
1. Check via `inspect_agents`.
2. If missing file/type → create it, re-dispatch.
3. If conceptual blocker → cancel via `cancel_run`, implement self, or Step 0.
4. If blocker reveals plan flaw → `cancel_run` all, return to Step 0.

Do not let a sub-agent spin >2 min. Timebox, assess, act.
