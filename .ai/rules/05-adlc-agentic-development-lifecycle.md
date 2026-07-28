# ADLC — Agentic Development Lifecycle

**⚠️ THIS IS THE MANDATORY PROCESS FOR ALL WORK IN THIS REPO.**
Read this file before starting any task. See also `AGENTS.md` ("Mandatory process" section) and `.ai/INDEX.md` ("MANDATORY" section).

**Scope**: All feature work, bug fixes, refactors, and cross-package changes in this repo.
**Override**: This rule governs *how* work is sequenced and verified. It does not override `AGENTS.md`, `.ai/rules/00-operating-doctrine.md`, or `.ai/doctrines/*`.

**Fast Path**: Trivial changes (≤5 lines, single file, no new types) may skip Steps 0-3. If unsure, use the full ADLC.

---

## Research-Backed Principles

These principles are drawn from industry experience with LLM-based coding agents (Addy Osmani, Martin Fowler/ThoughtWorks, Vellum AI, and multi-agent framework research 2024-2025):

1. **TDD — tests before code.** Every unit of work has its test written first. The RED test MUST compile and fail an assertion — not just fail with "undefined function". Martin Fowler's team found that "LLMs declare success in spite of red tests" — our RED phase requires explicit assertion-failure evidence.

2. **Micro-tasks for agents.** Research shows LLM generation quality degrades the longer a session runs ("hit and miss the longer a session becomes" — Fowler). "Restart coding sessions as frequently as possible" is the common advice. Our micro-tasks enforce a fresh context window per task. Each task is **1 function OR 1 file**, not both. Typical task: 15-30 lines production code + 30-60 lines test code (~50-100 lines total). No task exceeds 1 file.

3. **Deterministic checkpoints.** Fowler's team found that LLMs "find ways to get around" soft gates. Our wave gates are hard: `go build` + `go test -race` must pass before the next wave starts. No exceptions.

4. **Challenge before build.** Every plan is attacked before any code is written. Every task is validated before implementation.

5. **Test-drive the bug audit.** When uncertain about a bug report, write a test first. The test decides.

6. **Fail fast, roll back.** If a step reveals a plan flaw, return to Step 0. Do not "patch around" a bad plan.

7. **Idempotency.** Every step must be safely re-runnable with no side effects on success.

---

## Test Types in ADLC

| Type | When | Who | Scope | Gate |
|------|------|-----|-------|------|
| **Unit test (RED)** | Step 4a, per micro-task | Implementing sub-agent | Single function/type. Tests the new API surface. MUST compile AND fail an assertion. | `go test -run TestXxx ./pkg/...` fails with assertion failure (not compile error) |
| **Unit test (GREEN)** | Step 4b, same micro-task | Same agent | Production code that makes the RED test pass. | `go test -run TestXxx ./pkg/...` passes |
| **Integration test** | Step 4c, after all micro-tasks in a wave | Dedicated sub-agent | End-to-end path through multiple layers. Tests the contract. | `go test -run TestIntegration ./...` passes |
| **Invariant test** | Before Step 0 + Step 4 | Orchestrator | Existing invariant manifest. Ensures no regression in sensitive packages. | `make invariants` passes |
| **Regression test** | Step 5 (bug audit) | Hostile auditor | Proves a reported bug does NOT exist (false positive guard). | Test passes → rejected. Test fails → confirmed bug. |
| **Race test** | After every wave + Step 6 | Orchestrator | Detects data races. | `go test -race ./affected...` passes |

**TDD Rule**: Every micro-task of type "production code" must have a preceding micro-task of type "test" in the same wave. The test is written FIRST (RED phase — must compile and fail an assertion). The production code is written second (GREEN phase). No production code is committed without its test.

---

## Templates

All ADLC artifacts use standardised templates. Every agent MUST produce output matching the relevant template. Deviations are grounds for REJECT at the next gate. Template compliance is validated in Step 3.

**Template version**: `v1`. If templates are updated, the template version field in each artifact tracks which version was used.

### Template: Plan

```markdown
# Plan: <name>
Template-Version: v1

## Goal
One sentence.

## Scope
- **In scope**: bullet list
- **Out of scope**: bullet list
- **Boundary**: packages/files this plan may touch

## Files to Create
- `path/to/file.go` — what it contains

## Files to Modify
- `path/to/file.go` — one-line summary of change

## API Surface
```go
// Exact Go signatures
```

## Dependency Graph
```
Wave 1: [t1, t2] — tests
Wave 2: [t3, t4] — implementation + reviewer
```

## Test Strategy
| Test Name | Type | Scenario | Expected RED Failure |
|-----------|------|----------|---------------------|

## Plan Scorecard
| Criterion | Score | Notes |
|-----------|-------|-------|

## Rollback Criterion
Condition that kills this plan.

## Disposition Log
| Finding | Source | Verdict | Rationale |
|---------|--------|---------|-----------|
```

### Template: Task

```markdown
## Task: <ID> — <title>
- **Wave**: N
- **File**: `path/to/file.go` (create|modify)
- **Type**: test | prod | review | integration
- **API**: `func Name(args) (Result, error)`
- **Depends on**: t1, t2
- **Verification**: `go test -run TestXxx ./pkg/...`
- **Timeout**: 120s
- **Context scope**: `internal/pkg/a.go`, `internal/pkg/b.go`
```

### Template: Task List (tasks.md)

```markdown
# Tasks: <plan-name>

## Wave 1
- t1: ...
- t2: ...

## Wave 2
- t3: ... [depends: t1]
```

### Template: Validation Report

```markdown
# Validation: <plan-name> Wave N

## Wave N
- t1: PASS | REJECT — reason
- t2: PASS | REJECT — reason
```

### Template: Bug Audit Report

```markdown
## Round N — Agent M
- **Finding**: description
- **Severity**: HIGH | MEDIUM | LOW
- **File**: `path/file.go:line`
- **Status**: confirmed | rejected | uncertain
- **Evidence**: test output or rationale
```

### Template: Disposition Log

```markdown
| # | Source | Finding | Severity | Verdict | Rationale |
|---|--------|---------|----------|---------|-----------|
```

### Template: Handoff (for sub-agent transitions)

When one sub-agent finishes a micro-task and another picks up the next, the orchestator writes a handoff note:

```markdown
## Handoff: <task-ID> → <next-task-ID>
- **State**: files written, tests passing, any known issues
- **Working tree**: `git status --short` summary
- **Unresolved**: list of decisions deferred
- **Context**: files the next agent MUST read
```

### Template: Error/BLOCKED Report

```markdown
## BLOCKED: <task-ID>
- **Agent**: <name>
- **Duration**: N minutes stuck
- **Error**: exact compiler/test error
- **Attempted fixes**: list
- **Requested help**: what the orchestrator should do
```

### Template: Reviewer Output

```markdown
## Review: <task-ID>
- **Reviewed file(s)**: `path/file.go`
- **Verdict**: PASS | REJECT | PASS_WITH_COMMENTS
- **Issues**: bullet list of findings
- **Verification**: `go build + go test` result
```

---

## Artifact Directory

```
.ai/plan/<name>/
├── plan.md              # Locked plan
├── tasks.md             # Micro-task breakdown
├── validation.md        # Validation results
├── evidence/
│   ├── ledger.md        # Codebase state before plan
│   ├── challenge-01.md
│   ├── disposition.md
│   └── red-<id>.log     # RED phase test failures
├── audit/
│   ├── round-01.md
│   └── round-02.md
├── handoff-<id>.md      # Handoff notes between agents
└── done                 # Empty marker
```

---

## File Conflict & Ownership Rules

Two tasks in different waves **must not** touch the same file. If Wave 1 writes to `foo.go`, Wave 2 cannot modify `foo.go`. This prevents merge conflicts.

If a file needs changes from multiple waves, the plan must specify:
- Wave 1 creates `foo.go` with the interface
- Wave 2 creates `foo_test.go` (different file, no conflict)
- Wave 3 integrates via a NEW file, not modifying `foo.go`

**Exception**: Reviewer and audit tasks may read any file but write only to `.ai/plan/<name>/`.

---

## Protocol (7 Steps — no skipping, no reordering)

### Step 0 — Plan, Challenge & Lock

**Who**: Orchestrator agent (you).
**Duration cap**: 20 minutes or 3 sub-agent tasks.
**Produces**: `.ai/plan/<name>/plan.md` + evidence/.

**Actions**:

1. `mkdir -p .ai/plan/<name>/evidence .ai/plan/<name>/audit`

2. **Read codebase + invariants** if touching sensitive packages.
   - **File conflict check**: `ls <proposed-new-paths>` — if exists, plan must modify, not create.

3. **Evidence ledger** → `.ai/plan/<name>/evidence/ledger.md`.

4. **Write plan** → `.ai/plan/<name>/plan.md` using the Plan template.

   **Scorecard criteria:**
   1. All existing tests will still pass (verified by understanding the change)
   2. No new import cycles
   3. No breaking changes to existing public API
   4. New code is testable in isolation (no global state, no required network)
   5. Config changes are backward-compatible
   6. Every new public function has ≥1 named test scenario
   7. Integration test path is identified
   8. No file is touched by >1 wave (file ownership rule)

5. **Dispatch 2-4 challenge agents.** They receive plan + ledger only.
   - Prompt MUST include: *"Write your complete report to `.ai/plan/<name>/evidence/challenge-<N>.md`. Include severity (HIGH/MEDIUM/LOW) and exactly what in the plan is wrong."*
   - After all agents complete, **verify files exist**: `ls .ai/plan/<name>/evidence/challenge-*.md`. If any missing, re-dispatch. Do not proceed without all outputs on disk.

6. **Disposition** → `evidence/disposition.md`. Re-score scorecard.
   Any FAIL → plan rejected. Return to action 4.

7. **Lock plan.** No further edits without returning to Step 0.

**Gate**: All challenges dispositioned. Zero unaddressed HIGH. Scorecard all PASS. File-conflict check passed.

---

### Step 1 — Micro-Task Breakdown

**Who**: Orchestrator.
**Duration cap**: 10 minutes.
**Produces**: `.ai/plan/<name>/tasks.md`.

**Rules**:
- **1 file per task.** No task creates or modifies more than 1 file.
- **1 function per production task.** If a file needs 3 functions, that's 3 separate tasks.
- **Test tasks precede production tasks.** For every production task, there's a test task in the same wave that writes the RED test first.
- **Reviewer tasks every 2-3 implementation tasks.** Placed in the NEXT wave.
- **Context scope ≤ 5 files.** No sub-agent receives more than 5 files to read. This prevents context window overflow.

**Task format** (use the Task template above). Every task includes: ID, Wave, File, Type, API, Depends, Verification, Timeout, Context scope.

**Wave structure:**
- Wave 1: test tasks for foundation layer
- Wave 2: implementation + reviewer for Wave 1
- Wave 3: integration tests for the layer
- Repeat for each dependency layer

**Gate**: 1 file per task. 1 function per production task. Test task pairs with each production task. Reviewer every 2-3 impl. Context scope ≤5 files.

---

### Step 2 — Validate Each Task

**Who**: Parallel sub-agents (1 per wave).
**Duration cap**: 3 minutes per validator.
**Produces**: `.ai/plan/<name>/validation.md`.

**Actions**:

1. One validator per wave. Prompt: *"Validate these micro-tasks. Read the context scope files. Can each task be implemented as described? Is the RED test achievable (compiles, fails assertion)? Are boundaries correct (1 file, 1 function)? Output PASS or REJECT per task. Write your validation to `.ai/plan/<name>/validation-w<N>.md`."*
2. Validator reads actual Go files from context scope.
3. After validators complete, **verify files exist**: `ls .ai/plan/<name>/validation-*.md`. Collate into `validation.md`. If any missing, re-dispatch.

**Gate**: All PASS. Any REJECT → Step 1. 2nd REJECT on same task → Step 0.

---

### Step 3 — Verify & Finalize

**Who**: Orchestrator.
**Duration cap**: 5 minutes.
**Produces**: Locked `tasks.md`.

**Actions**:

1. Read all validation outputs.
2. **Template compliance check**: verify every artifact so far matches its template (Plan, Task). If any required field is missing or placeholder, fix it.
3. **Idempotency check**: verify no proposed file path already exists (unless "modify").
4. Lock `tasks.md`. No further edits.

**Gate**: Templates compliant. Idempotency check passed. Task list immutable.

---

### Step 4 — Orchestrate Implementation (TDD)

**Who**: Orchestrator + parallel sub-agents.
**Produces**: Working code in tree. Evidence of RED failures.

**Per micro-task: RED → GREEN → handoff**

1. **RED phase** (test tasks only):
   - Write a test that compiles and FAILS an assertion on the target API.
   - Verification: `go test -run TestXxx ./pkg/...` → assertion failure (NOT compile error).
   - Save evidence to `.ai/plan/<name>/evidence/red-<id>.log`.
   - **Do NOT write production code in a RED task.** If a sub-agent writes production code in a RED task, the task is rejected and must be redone.

2. **GREEN phase** (production tasks only):
   - Write MINIMAL production code that makes the RED test pass.
   - Verification: `go test -run TestXxx ./pkg/...` → PASS.
   - No extra code beyond what's needed to pass the test.

3. **Handoff**: After each task, the orchestrator writes a handoff note (`handoff-<id>.md`) summarizing: files written, test status, any deferred decisions. The next sub-agent reads this before starting.

**Wave execution:**

1. Execute waves **in order**. Wave N never starts until Wave N-1 gates pass.
2. Within a wave, tasks with no dependencies run in parallel.
3. **Reviewer tasks** in Wave N read Wave N-1 code. Must output `PASS` or `REJECT` using the Reviewer template. REJECT blocks the wave — orchestrator must fix before proceeding.
4. Sub-agents BLOCKED >2 min → use Error/BLOCKED template. Orchestrator responds via escalation protocol.
5. **Wave gate:**
   - `go build ./...` passes
   - `go test -race ./<all-affected>/...` passes
   - Integration tests for completed layers pass
   - Cross-wave check: all packages touched by any wave so far
   - If wave fails: quick fix (<5 lines) → apply and proceed. Plan flaw → Step 0.

**Gate**: All waves pass. RED phase evidence logged. GREEN phase tests pass. Handoff notes written for each sub-agent transition.

---

### Step 5 — Bug Audit Loop

**Who**: Orchestrator + 3-4 hostile sub-agents.
**Duration cap**: 3 rounds default, 5 max.
**Produces**: `.ai/plan/<name>/audit/round-<N>.md`.

**Actions**:

1. Dispatch auditors. Prompt: *"Find bugs, races, data loss, panics, contract violations. Report severity + file:line. Use Bug Audit Report template. Write your report to `.ai/plan/<name>/audit/round-<N>-agent-<M>.md`."*
   - After all auditors complete, **verify files exist**: `ls .ai/plan/<name>/audit/round-*.md`. If any missing, re-dispatch.
2. Per finding: confirmed→fix, rejected→write test as proof, uncertain→write test to decide.
3. Loop until zero bugs OR 5 rounds (→ plan rejected with evidence).
4. Regression guard: if fix breaks tests, halt+revert+re-analyse.
5. Blast-radius: audit only files changed THIS cycle.

**Gate**: All auditors report zero bugs. `go test -race ./...` passes all packages.

---

### Step 6 — Commit & Push

**Who**: Orchestrator.
**Duration cap**: 5 minutes.
**Produces**: Commit + push + `.ai/plan/<name>/done`.

**Actions**:

1. **Diff review**: check for debug code, secrets, out-of-scope files, binaries.
2. **TDD audit**: verify every production file has a corresponding `_test.go`. If missing, return to Step 4.
3. **Template completeness audit**: verify all artifacts in `.ai/plan/<name>/` are non-empty and template-compliant.
4. **Final verification**: `go build ./... && go vet ./... && go test -race ./...`
5. Conventional commit (`type(scope): subject`, ≤72 chars).
6. Body: what changed, why, verification status, link to plan directory.
7. `git push`.
8. `touch .ai/plan/<name>/done`.

**Gate**: Push succeeds. Tree clean. Diff review passed. Every production file has its test. All artifacts are template-compliant.

---

## Fast Path (trivial changes)

**Trivial** = ≤5 lines, single file, no new types, no config, no test file creation.
- Skip Steps 0-3. Implement in Step 4 directly + write test.
- Step 5: 1 hostile auditor (not 3-4).
- Step 6: normal commit.

Also valid for **reviewer-only changes** (reviewer found an issue, fix is trivial).

---

## Rejection & Rollback Rules

| Condition | Action |
|-----------|--------|
| Step 0 challenge reveals fundamental design flaw | Plan **rejected**. Start over. |
| Step 0 scorecard has any FAIL | Plan **rejected**. |
| Step 2 validator REJECTs | Return to Step 1. 2nd REJECT on same task → Step 0. |
| Step 3 template compliance fails | Fix templates. Re-validate. |
| Step 4 RED phase missing (test not written first) | Task rejected. Redo with test first. |
| Step 4 RED test doesn't compile (just "undefined") | Task rejected. Write assertion-failing test. |
| Step 4 reviewer REJECTs | Orchestrator fixes. If fix >5 lines → return to Step 1. |
| Step 4 wave fails — plan flaw | Return to Step 0. |
| Step 4 wave fails — quick fix | Apply <5 lines, re-verify, proceed. |
| Step 4 handoff missing | Halt. Write handoff. Do not proceed. |
| Step 5 audit loop >5 rounds | Plan **rejected**. Return to Step 0 with evidence. |
| Step 5 fix breaks existing tests | Halt. Revert. Re-analyse. |
| Step 6 missing test for production file | Return to Step 4. Do not commit. |
| Step 6 template audit fails | Fix artifacts. Re-verify. |
| Any step discovers regression | Halt, revert, Step 0. |

---

## Invariant Enforcement

Before Step 0 and Step 4, read `.ai/invariants.md` + run invariant tests if touching: `internal/cli/`, `internal/tools/`, `internal/agent/`, `internal/chat/`, `internal/config/`, `internal/ledger/`, `internal/coordinator/`, `internal/events/`, `internal/storage/`.

If invariant fails → blocked until restored or manifest updated with `Invariant-Update: INV-XX <reason>`.

---

## Escalation Protocol

Sub-agent BLOCKED >2 min → uses Error/BLOCKED template. Orchestrator reads and:
1. Missing file/type → create it, unblock, continue.
2. Conceptual blocker → cancel agent, implement self, or Step 0.
3. Blocker reveals plan flaw → cancel all, Step 0.

---

## Artifact Chain

```
Step 0 → plan.md (locked + scorecard)
         evidence/ledger.md, challenge-*.md, disposition.md
Step 1 → tasks.md (micro-tasks)
Step 2 → validation.md (PASS/REJECT)
Step 3 → tasks.md immutable
Step 4a→ evidence/red-<id>.log (RED test failures)
Step 4b→ working code in tree
Step 4 → handoff-<id>.md (per sub-agent transition)
Step 5 → audit/round-*.md
Step 6 → git commit + push + done
```
