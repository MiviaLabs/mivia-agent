# ADLC — Agentic Development Lifecycle

**Scope**: All feature work, bug fixes, refactors, and cross-package changes in this repo.
**Override**: This rule governs *how* work is sequenced and verified. It does not override `AGENTS.md`, `.ai/rules/00-operating-doctrine.md`, or `.ai/doctrines/*`.

---

## Protocol (6 Steps — no skipping, no reordering)

### Step 0 — Plan, Challenge & Lock

**Who**: Orchestrator agent (you).
**Duration cap**: 20 minutes or 3 sub-agent tasks, whichever comes first.

**Actions (in order)**:
1. **Read the codebase first.** Read every relevant file — the interface/contract being implemented, ALL existing implementations, the callers, the tests, the config wiring, and any existing plans under `.ai/plans/`. If you touch `internal/cli/`, `internal/tools/`, `internal/agent/`, `internal/chat/`, `internal/config/`, `internal/ledger/`, `internal/coordinator/`, `internal/events/`, or `internal/storage/`, also read `.ai/invariants.md` and run the invariant tests.
2. **Build an evidence ledger.** Before writing the plan, record what you discovered about the current codebase state (what exists, what doesn't, what patterns are used). This prevents the plan from being written in ignorance.
3. **Write the plan.** It MUST contain:
   - Goal (one sentence)
   - Files to create (exact paths)
   - Files to modify (exact paths, exact changes)
   - API surface (exact function/type signatures for new code)
   - Dependency graph (which pieces depend on which)
   - Test strategy (specific test scenarios, not just "add tests")
   - Rollback criterion (what condition would kill this plan)
4. **Dispatch 2-4 parallel challenge agents.** Prompt: *"You are a hostile reviewer. Find gaps, missing edge cases, wrong assumptions, risky dependencies, missing test scenarios, or incorrect API choices in this plan. Do NOT confirm — attack. Report each finding with severity (HIGH/MEDIUM/LOW) and exactly what in the plan is wrong."*
5. **Disposition every challenge output:**
   - Confirmed gap → update the plan. Record the fix.
   - Rejected challenge → write a one-line rationale. Do not ignore.
   - Competing challenges (two agents disagree) → the orchestrator decides and documents why.
6. **Lock the plan.** Do not deviate during implementation. If a blocking discovery occurs mid-implementation, pause, return to Step 0, update the plan, and re-lock. Do not "just fix it" outside the plan.

**Gate**: All challenge outputs read and explicitly dispositioned. Zero unaddressed HIGH challenges. Plan is saved to `.ai/plans/<name>.md`.

---

### Step 1 — Task Breakdown (1-3SP)

**Who**: Orchestrator agent.
**Duration cap**: 10 minutes.

**Actions**:
1. Slice the locked plan into tasks of **1-3 story points** (1 SP ≈ 1 file or 1 focused function group). If a task exceeds 3 SP, split it.
2. Declare dependency waves:
   - **Wave N** tasks depend on Wave N-1.
   - Tasks within the same wave must have zero dependency on each other.
3. Every task must specify:
   - Exact file path(s) to create or modify
   - Exact function/type signatures (copy from the locked plan)
   - Verification command that proves it works (e.g., `go build ./pkg/...`)
   - Maximum sub-agent timeout for this task

**Gate**: No task exceeds 3 SP. Dependency graph is acyclic and explicit (Wave 1 → Wave 2 → …). Every task has a verification command.

---

### Step 2 — Validate Each Task

**Who**: Parallel sub-agents (one per task or one per wave for small tasks).
**Duration cap**: 5 minutes per validator.

**Actions**:
1. Dispatch 1 validation agent per wave. Prompt: *"You are validating this task definition. Read the existing code it interfaces with. Can this task be implemented as described? Are there import cycles, missing types, wrong assumptions about existing APIs, or hidden dependencies? Output PASS or REJECT with specific reasons."*
2. The validator must read the actual Go files the task will touch — not just the task description.
3. Each validator outputs **PASS** or **REJECT** with file-level evidence.

**Gate**: All validators PASS. Any REJECT returns to Step 1 (re-breakdown). A second REJECT on the same task (after re-breakdown) escalates to Step 0 — the plan is wrong.

---

### Step 3 — Verify & Finalize

**Who**: Orchestrator agent.
**Duration cap**: 5 minutes.

**Actions**:
1. Read all validation outputs.
2. Make any required adjustments to task boundaries or signatures.
3. Lock the final task list. No further changes without returning to Step 0.

**Gate**: Task list is immutable after this step. Saved alongside the plan.

---

### Step 4 — Orchestrate Implementation

**Who**: Orchestrator + parallel sub-agents.
**Duration cap**: No hard cap, but each sub-agent gets a finite timeout set in Step 1.

**Actions**:
1. Execute waves **in order** (Wave 1 → Wave 2 → …). Never start Wave N until Wave N-1 passes gate.
2. Within a wave, dispatch all tasks in **parallel**.
3. Each sub-agent implementation MUST:
   - Read the existing code it touches (interfaces, callers, tests).
   - Read `.ai/invariants.md` if in a sensitive package.
   - Write production code.
   - Run `go build ./<package>/...`
   - Run `go vet ./<package>/...`
   - Run `go test -count=1 ./<package>/...`
   - Fix ALL compilation errors and test failures before reporting DONE.
   - If stuck for >2 minutes, report BLOCKED with specific error. Do not spin.
4. **Orchestrator gate check after each wave:**
   - `go build ./...` passes.
   - `go test -count=1 -race ./<all-affected-packages>/...` passes.
   - If the wave fails, do NOT start the next wave. Assess: is it a quick fix (do it yourself) or a plan flaw (return to Step 0)?
   - **Quick fix** (< 5 lines, no structural change): apply it directly, re-verify, proceed.
   - **Plan flaw** (needs new types, new files, different API): return to Step 0.
5. If a sub-agent's output has compilation errors, close them yourself. Do not dispatch a replacement agent for the same task — the orchestrator owns the gap.

**Gate**: All waves pass `go build ./...` + `go test -race ./<affected>`. Working tree compiles and all existing tests still pass.

---

### Step 5 — Bug Audit Loop

**Who**: Orchestrator + parallel hostile sub-agents.
**Duration cap**: 3 rounds by default, 5 rounds max (hard stop).

**Actions**:
1. Dispatch **3-4 parallel hostile audit agents** across the changed code. Prompt: *"You are a hostile reviewer. Find bugs, races, data loss, panics, or contract violations. Report with severity (HIGH/MEDIUM/LOW) and exact file:line. Do NOT confirm — attack."*
2. Each auditor must read the actual code files — not summaries.
3. For each finding, the orchestrator does ONE of:
   - **Confirmed**: fix the bug immediately, re-run `go test -race ./...`, commit.
   - **Rejected with rationale**: write a code comment or test that proves the finding is not a bug. Document the rationale in the commit message.
   - **Uncertain**: write a targeted test that would expose the bug. If the test passes → rejected. If it fails → confirmed and fixed.
4. **Loop**: After fixing all confirmed bugs, dispatch auditors again (round N+1). Repeat until:
   - All auditors report zero bugs → proceed to Step 6.
   - OR 5 rounds elapse with bugs still being found → plan is rejected. Return to Step 0 with the accumulated bug list as evidence.
5. Between audit rounds, if a fix introduces a regression in existing tests, halt immediately. Revert the fix, re-analyse, and re-apply correctly. Do not "push through" a failing existing test.

**Gate**: All auditors report zero bugs. `go test -race ./...` passes on ALL packages (not just affected ones).

---

### Step 6 — Commit & Push

**Who**: Orchestrator agent.
**Duration cap**: 5 minutes.

**Actions**:
1. Final verification: `go build ./... && go vet ./... && go test -race ./...`.
2. Write a conventional commit message (`type(scope): subject`) following `.ai/rules/80-commit-message.md`.
3. Commit body MUST include: what was changed, why (reference bug/plan/issue), and verification status.
4. `git push`.

**Gate**: Push succeeds. Working tree is clean. Commit references the plan or bug.

---

## Rejection & Rollback Rules

| Condition | Action |
|-----------|--------|
| Step 0 challenge reveals a fundamental design flaw | Plan is **rejected**. Do not proceed to Step 1. Start over at Step 0 with corrected assumptions. |
| Step 2 validator REJECTs | Return to Step 1 (re-breakdown). Second REJECT on same task → return to Step 0. |
| Step 4 wave fails build/tests, and it's a plan flaw | Return to Step 0. The plan was wrong. |
| Step 4 wave fails build/tests, and it's a quick fix | Apply fix directly, re-verify, proceed. |
| Step 5 audit loop exceeds 5 rounds | Plan is **rejected**. Return to Step 0 with bug evidence. |
| Step 5 fix breaks existing tests | Halt. Revert fix. Re-analyse. Do not push through. |
| Any step discovers a regression in existing behaviour | Immediately halt. Revert the change. Return to Step 0. The plan was wrong. |

## Invariant Enforcement

Before Step 0 (reading code) and before Step 4 (writing code), the orchestrator **must** read `.ai/invariants.md` if the change touches any of these packages:

- `internal/cli/`
- `internal/tools/`
- `internal/agent/`
- `internal/chat/`
- `internal/config/`
- `internal/ledger/`
- `internal/coordinator/`
- `internal/events/`
- `internal/storage/`

If an invariant test fails during Step 4, the implementation is **blocked** until the invariant is restored or the manifest is updated with `Invariant-Update: INV-XX <reason>`. Do not proceed past a failing invariant.

## Escalation

If any sub-agent reports BLOCKED (stuck >2 minutes with no progress):

1. Orchestrator reads the blocker.
2. If it's a missing file/type the orchestrator can create: do it, unblock, sub-agent continues.
3. If it's a conceptual blocker (don't know how to implement the API): cancel the sub-agent, implement it yourself, or return to Step 0.

Do not let a sub-agent spin for >2 minutes. Timebox, assess, act.
