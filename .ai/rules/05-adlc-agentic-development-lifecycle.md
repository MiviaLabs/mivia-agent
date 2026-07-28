# ADLC — Agentic Development Lifecycle

**Scope**: All feature work, bug fixes, refactors, and cross-package changes in this repo.
**Override**: This rule governs *how* work is sequenced and verified. It does not override `AGENTS.md`, `.ai/rules/00-operating-doctrine.md`, or `.ai/doctrines/*`.

---

## Principles

1. **Every step produces a durable artifact.** The next step consumes the artifact of the previous. No knowledge is lost between steps.
2. **Challenge before build.** Every plan is attacked before any code is written. Every task definition is validated before any implementation starts.
3. **Test-drive the bug audit.** When uncertain about a bug report, write a test first. The test decides.
4. **Fail fast, roll back.** If a step reveals a plan flaw, return to Step 0. Do not "patch around" a bad plan.
5. **Idempotency.** Every step must be safely re-runnable with no side effects on success.

---

## Protocol (6 Steps — no skipping, no reordering)

### Step 0 — Plan, Challenge & Lock

**Who**: Orchestrator agent (you).
**Duration cap**: 20 minutes or 3 sub-agent tasks, whichever comes first.
**Produces**: `.ai/plans/<name>-v1.md` (the locked plan).

**Actions (in order)**:

1. **Read the codebase first.**
   - Read every relevant file — the interface/contract being implemented, ALL existing implementations, the callers, the tests, the config wiring, and any existing plans under `.ai/plans/`.
   - If the change touches `internal/cli/`, `internal/tools/`, `internal/agent/`, `internal/chat/`, `internal/config/`, `internal/ledger/`, `internal/coordinator/`, `internal/events/`, or `internal/storage/`, also read `.ai/invariants.md` and run the invariant tests.
   - **Verify no file conflicts**: run `ls <proposed-new-file-paths>` to ensure you're not overwriting something that already exists. If a file exists, the plan must account for it (modify, not create).

2. **Build an evidence ledger.**
   - Record the current state of every file the plan will touch (what exists, what doesn't, key patterns used).
   - This prevents the plan from being written in ignorance and provides a rollback baseline.

3. **Write the plan.** It MUST contain all of these sections:

   | Section | Required Content |
   |---------|-----------------|
   | **Goal** | One sentence. |
   | **Files to create** | Exact paths. |
   | **Files to modify** | Exact paths + one-line summary of each change. |
   | **API surface** | Exact Go function/type signatures for new public/exported code. |
   | **Dependency graph** | Which pieces depend on which (Wave 1 → Wave 2 → …). |
   | **Test strategy** | Named test scenarios with expected outcomes. Not just "add tests". |
   | **Plan scorecard** | Self-score against these criteria: (1) All existing tests still pass, (2) No new import cycles, (3) No breaking changes to existing public API, (4) New code is testable in isolation, (5) Config changes are backward-compatible, (6) Error paths are handled. Score PASS/FAIL per criterion. |
   | **Rollback criterion** | What condition would kill this plan (e.g., "if SQLite WAL mode doesn't support concurrent writers, this plan is invalid"). |

4. **Dispatch 2-4 parallel challenge agents.**
   - Prompt: *"You are a hostile reviewer. Find gaps, missing edge cases, wrong assumptions, risky dependencies, missing test scenarios, or incorrect API choices in this plan. Do NOT confirm — attack. Report each finding with severity (HIGH/MEDIUM/LOW) and exactly what in the plan is wrong."*
   - Each agent receives ONLY the plan + the evidence ledger. Not the entire codebase.

5. **Disposition every challenge output:**
   - **Confirmed gap** → update the plan. Record the fix and increment the plan version (`-v2`, `-v3`).
   - **Rejected challenge** → write a one-line rationale. Do not ignore.
   - **Competing challenges** (two agents disagree) → the orchestrator decides and documents why.
   - After disposition: re-score the plan scorecard. If any criterion dropped from PASS to FAIL, the plan is rejected.

6. **Lock the plan.**
   - Save the final version to `.ai/plans/<name>-v<N>.md` with the disposition log appended.
   - Do not deviate during implementation. If a blocking discovery occurs mid-implementation, pause, return to Step 0, and produce a new plan version.

**Gate**: All challenge outputs read and explicitly dispositioned. Zero unaddressed HIGH challenges. Plan scorecard: all PASS. File conflict check passed.

---

### Step 1 — Task Breakdown (1-3SP)

**Who**: Orchestrator agent.
**Duration cap**: 10 minutes.
**Produces**: Task list appended to the plan file (`.ai/plans/<name>-v<N>.md`).

**Actions**:

1. Slice the locked plan into tasks of **1-3 story points** (1 SP ≈ 1 file or 1 focused function group). If a task exceeds 3 SP, split it.
2. Declare dependency waves:
   - **Wave N** tasks depend on Wave N-1.
   - Tasks within the same wave must have zero dependency on each other.
3. Every task must specify:
   - Task ID (`t1`, `t2`, …)
   - Exact file path(s) to create or modify
   - Exact function/type signatures (copy from the locked plan)
   - Verification command that proves it works (e.g., `go build ./pkg/...`)
   - Maximum sub-agent timeout for this task
   - Context scope: the specific files the sub-agent needs to read (not the whole repo)

**Gate**: No task exceeds 3 SP. Dependency graph is acyclic and explicit (Wave 1 → Wave 2 → …). Every task has a verification command. Every task has a context scope.

---

### Step 2 — Validate Each Task

**Who**: Parallel sub-agents (one per wave).
**Duration cap**: 5 minutes per validator.
**Produces**: PASS/REJECT with file-level evidence for each wave.

**Actions**:

1. Dispatch 1 validation agent per wave. Prompt: *"You are validating this task definition. Read the existing code it interfaces with. Can this task be implemented as described? Are there import cycles, missing types, wrong assumptions about existing APIs, or hidden dependencies? Output PASS or REJECT with specific file-level reasons."*
2. The validator MUST read the actual Go files the task will touch — not just the task description. The context scope from Step 1 tells them what to read.
3. Each validator outputs **PASS** or **REJECT** with file-level evidence.

**Gate**: All validators PASS. Any REJECT returns to Step 1 (re-breakdown). A second REJECT on the same task (after re-breakdown) escalates to Step 0 — the plan is wrong.

---

### Step 3 — Verify & Finalize

**Who**: Orchestrator agent.
**Duration cap**: 5 minutes.
**Produces**: Immutable task list (locked in the plan file).

**Actions**:

1. Read all validation outputs.
2. Make any required adjustments to task boundaries or signatures.
3. **Final idempotency check**: verify that no proposed file path already exists (unless the task says "modify").
4. Lock the final task list. No further changes without returning to Step 0.

**Gate**: Task list is immutable after this step. Idempotency check passed (no accidental overwrites).

---

### Step 4 — Orchestrate Implementation

**Who**: Orchestrator + parallel sub-agents.
**Duration cap**: No hard cap on the orchestrator, but each sub-agent gets the timeout set in Step 1.
**Produces**: Working code in the tree.

**Actions**:

1. Execute waves **in order** (Wave 1 → Wave 2 → …). Never start Wave N until Wave N-1 passes gate.
2. Within a wave, dispatch all tasks in **parallel**.
3. Each sub-agent implementation MUST:
   - Read the context scope files assigned in Step 1.
   - Read `.ai/invariants.md` if in a sensitive package.
   - Write production code.
   - Run `go build ./<package>/...`
   - Run `go vet ./<package>/...`
   - Run `go test -count=1 ./<package>/...`
   - Fix ALL compilation errors and test failures before reporting DONE.
   - **If stuck for >2 minutes**: report BLOCKED with the exact error. Do not spin.
   - **Do not modify files outside the assigned path list.**

4. **Orchestrator wave gate check (after each wave):**
   - `go build ./...` passes.
   - `go test -count=1 -race ./<all-affected-packages>/...` passes.
   - **Cross-wave integration check**: run `go test -count=1 -race ./<packages-from-previous-waves + current-wave>/...` to ensure nothing regressed.
   - If the wave fails:
     - **Quick fix** (< 5 lines, no structural change): apply it directly, re-verify, proceed.
     - **Plan flaw** (needs new types, new files, different API): return to Step 0. Do not "just fix it" outside the plan.

5. If a sub-agent's output has compilation errors, close them yourself. Do not dispatch a replacement agent for the same task — the orchestrator owns the gap.

**Gate**: All waves pass `go build ./...` + `go test -race ./<all-packages-touched-by-any-wave>`. Working tree compiles. All existing tests still pass.

---

### Step 5 — Bug Audit Loop

**Who**: Orchestrator + parallel hostile sub-agents.
**Duration cap**: 3 rounds by default, 5 rounds max (hard stop).
**Produces**: Zero-bug confirmation + final code.

**Actions**:

1. Dispatch **3-4 parallel hostile audit agents** across the changed code. Prompt: *"You are a hostile reviewer. Find bugs, races, data loss, panics, or contract violations. Report with severity (HIGH/MEDIUM/LOW) and exact file:line. Do NOT confirm — attack."*
2. Each auditor MUST read the actual code files — not summaries.
3. For each finding, the orchestrator does ONE of:
   - **Confirmed**: fix the bug immediately, re-run `go test -race ./...`, commit the fix.
   - **Rejected with rationale**: write a code comment or a targeted test that proves the finding is not a bug. Commit the test. Document the rationale in the commit message.
   - **Uncertain**: write a targeted test that would expose the bug. If the test passes → rejected. If it fails → confirmed and fixed.
4. **Loop**: After fixing all confirmed bugs, dispatch auditors again (round N+1). Repeat until:
   - All auditors report zero bugs → proceed to Step 6.
   - OR 5 rounds elapse with bugs still being found → plan is **rejected**. Return to Step 0 with the accumulated bug list as evidence. Do not proceed to Step 6.
5. **Regression guard between audit rounds**: if a fix introduces a regression in existing tests, halt immediately. Revert the fix, re-analyse, and re-apply correctly. Do not "push through" a failing existing test.
6. **Blast-radius scope**: each audit round covers only the files changed in THIS cycle plus any files those changes touch. Do not re-audit the entire codebase.

**Gate**: All auditors report zero bugs. `go test -race ./...` passes on ALL packages (not just affected ones).

---

### Step 6 — Commit & Push

**Who**: Orchestrator agent.
**Duration cap**: 5 minutes.
**Produces**: Commit on `master`, pushed to `origin`.

**Actions**:

1. **Diff review**: Read the complete diff (`git diff --cached` or `git diff HEAD`). Verify:
   - No debug code, print statements, or commented-out code.
   - No files modified outside the plan scope.
   - No credentials, secrets, or local paths in the diff.
   - No binary files committed by accident.
2. **Final verification**: `go build ./... && go vet ./... && go test -race ./...`.
3. Write a conventional commit message (`type(scope): subject`) following `.ai/rules/80-commit-message.md`. Subject ≤ 72 chars, no trailing period.
4. Commit body MUST include:
   - What was changed (one sentence per file group)
   - Why (reference bug ID, plan version, or issue number)
   - Verification status (e.g., "go test -race ./...: 18/18 pass")
5. `git push`.
6. **Post-push cleanup**: Archive the plan to `.ai/plans/archived/<name>-v<N>-done.md` or remove the active plan marker.

**Gate**: Push succeeds. Working tree is clean. Diff review passed. Commit references the plan.

---

## Rejection & Rollback Rules

| Condition | Action |
|-----------|--------|
| Step 0 challenge reveals a fundamental design flaw | Plan **rejected**. Do not proceed to Step 1. Start over at Step 0 with corrected assumptions. |
| Step 0 scorecard has any FAIL | Plan **rejected**. Do not proceed. |
| Step 2 validator REJECTs | Return to Step 1 (re-breakdown). Second REJECT on same task → return to Step 0. |
| Step 4 wave fails build/tests, plan flaw | Return to Step 0. The plan was wrong. |
| Step 4 wave fails build/tests, quick fix | Apply fix directly (< 5 lines), re-verify, proceed. |
| Step 5 audit loop exceeds 5 rounds | Plan **rejected**. Return to Step 0 with accumulated bug evidence. |
| Step 5 fix breaks existing tests | Halt. Revert fix. Re-analyse. Do not push through. |
| Step 6 diff review reveals out-of-scope changes | Halt. Revert. Do not push. Return to Step 0. |
| Any step discovers a regression in existing behaviour | Immediately halt. Revert the change. Return to Step 0. The plan was wrong. |

---

## Invariant Enforcement

Before Step 0 (reading code) and before Step 4 (writing code), the orchestrator **must** read `.ai/invariants.md` and run the corresponding invariant tests if the change touches any of:

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

---

## Escalation Protocol

If any sub-agent reports **BLOCKED** (stuck >2 minutes with no progress):

1. Orchestrator reads the blocker.
2. If it's a missing file/type the orchestrator can create: do it, unblock, sub-agent continues.
3. If it's a conceptual blocker (don't know how to implement the API): cancel the sub-agent, implement it yourself, or return to Step 0.
4. If the blocker reveals a flaw in the locked plan: cancel all sub-agents, return to Step 0.

Do not let a sub-agent spin for >2 minutes. Timebox, assess, act.

---

## Artifact Chain

Every step produces an artifact consumed by the next:

```
Step 0 → .ai/plans/<name>-v<N>.md       (locked plan + disposition log)
Step 1 → plan file appends task list      (waves + task specs)
Step 2 → plan file appends validation log (PASS/REJECT per wave)
Step 3 → plan file marked IMMUTABLE       (no further edits)
Step 4 → working code in tree             (changes to files)
Step 5 → bug evidence log                 (rounds, findings, fixes)
Step 6 → git commit + push                (done marker)
```

No step should need to re-discover information from a previous step.
