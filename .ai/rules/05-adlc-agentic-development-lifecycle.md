# ADLC — Agentic Development Lifecycle

**Scope**: All feature work, bug fixes, refactors, and cross-package changes in this repo.
**Override**: This rule governs *how* work is sequenced and verified. It does not override `AGENTS.md`, `.ai/rules/00-operating-doctrine.md`, or `.ai/doctrines/*`.

---

## Principles

1. **TDD — tests before code.** Every unit of work has its test written and passing RED (failing the new test) before any production code is written. Integration tests validate the full path after all units are done.
2. **Micro-tasks for agents.** No task exceeds ~50 lines of new code or 1 file. Large work is sliced into micro-tasks that are built, verified, and reviewed independently before the next sub-agent proceeds.
3. **Every step produces a durable artifact.** The next step consumes the artifact of the previous. No knowledge is lost between steps.
4. **Challenge before build.** Every plan is attacked before any code is written. Every task definition is validated before any implementation starts.
5. **Test-drive the bug audit.** When uncertain about a bug report, write a test first. The test decides.
6. **Fail fast, roll back.** If a step reveals a plan flaw, return to Step 0. Do not "patch around" a bad plan.
7. **Idempotency.** Every step must be safely re-runnable with no side effects on success.

---

## Test Types in ADLC

| Type | When | Who | Scope | Gate |
|------|------|-----|-------|------|
| **Unit test (RED)** | Step 4a, per micro-task | Implementing sub-agent | Single function/type. Tests the new API surface. Must fail on first run (no production code yet). | `go test -run TestXxx ./pkg/...` fails with expected error |
| **Unit test (GREEN)** | Step 4b, same micro-task | Same agent (or next) | Production code that makes the RED test pass. | `go test -run TestXxx ./pkg/...` passes |
| **Integration test** | Step 4c, after all micro-tasks in a wave | Orchestrator or dedicated sub-agent | End-to-end path through all layers. Tests the contract. | `go test -run TestIntegration ./...` passes |
| **Invariant test** | Before Step 0 + Step 4 | Orchestrator | Existing invariant manifest. Ensures no regression in sensitive packages. | `make invariants` passes |
| **Regression test** | Step 5 (bug audit) | Hostile auditor | Proves a reported bug does NOT exist (false positive guard). | Test passes → rejected. Test fails → confirmed bug. |
| **Race test** | After every wave + Step 6 | Orchestrator | Detects data races under `-race`. | `go test -race ./affected...` passes |

**TDD Rule**: Every micro-task of type "production code" must be paired with a preceding or same-micro-task "test" that validates the new API. The test is written FIRST (RED phase), then the production code is written to make it pass (GREEN phase). No production code is committed without its test.

---

## Templates

All ADLC artifacts use standardised templates. Every agent MUST produce output matching the relevant template. Deviations are grounds for REJECT at the next gate.

### Template: Plan (`plan.md`)

```markdown
# Plan: <name>

## Goal
One sentence describing what this plan achieves.

## Scope
- **In scope**: what this plan does (bullet list)
- **Out of scope**: what this plan explicitly does NOT do (bullet list)
- **Boundary**: what packages/files this plan is allowed to touch

## Files to Create
- `path/to/new/file.go` — one-line description of what it contains

## Files to Modify
- `path/to/existing/file.go` — one-line summary of the change

## API Surface
```go
// Exact Go signatures for all new exported types and functions
type NewType struct { ... }
func NewFunction(arg Type) (Result, error)
```

## Dependency Graph
```
Wave 1: [task-a, task-b]    # tests/foundations, no deps on each other
Wave 2: [task-c, task-d]    # depends on Wave 1
Wave 3: [task-e]             # integration tests
```

## Test Strategy
| Test Name | Type | Scenario | Expected RED Failure |
|-----------|------|----------|---------------------|
| `TestXxx` | unit | Valid input produces correct output | `undefined: NewFunction` |
| `TestYyy` | unit | Invalid input returns error | compiler error or assertion |
| `TestIntegrationZzz` | integration | Full path from API to storage | N/A (integration, no RED) |

## Plan Scorecard
1. Existing tests pass: PASS / FAIL
2. No new import cycles: PASS / FAIL
3. No breaking changes: PASS / FAIL
4. Testable in isolation: PASS / FAIL
5. Config backward-compatible: PASS / FAIL
6. Every new function has a named test: PASS / FAIL
7. Integration test path identified: PASS / FAIL

## Rollback Criterion
What condition kills this plan. Example: "If marshalling RunSnapshot produces a format that can't be read back on restart, this plan is invalid."
```

**✅ Positive example** (well-formed): The plan for Phase 3 StorageLedgerRepository at `.ai/plans/phase3-durable-persistence.md` — it names exact files, exact API signatures, wave dependency graph, test scenarios, and scorecard.

**❌ Negative example** (too vague): *"Goal: Add SQLite support. Approach: wrap the store. Test: add tests."* — no file paths, no signatures, no wave deps, no named test scenarios. This would be REJECTED at Step 0 challenge.

---

### Template: Task Breakdown (`tasks.md`)

```markdown
# Tasks: <name>

## Wave 1 — Tests/Foundations

### t1: [RED test] TestRunSnapshotJSONRoundTrip
- **Type**: test (RED)
- **File**: `internal/ledger/storage_test.go` (modify)
- **Scope**: Add one test function only. Do NOT modify any other file.
- **API under test**: `marshalRunSnapshot(RunSnapshot) ([]byte, error)`, `unmarshalRunSnapshot([]byte) (RunSnapshot, error)`
- **Expected RED failure**: `undefined: marshalRunSnapshot` (function doesn't exist yet)
- **Verification**: `go test -run TestRunSnapshotJSONRoundTrip ./internal/ledger/...` must FAIL
- **Timeout**: 60s
- **Context**: `internal/ledger/types.go`
- **Depends on**: none

### t2: [GREEN] marshalRunSnapshot + unmarshalRunSnapshot
- **Type**: production
- **File**: `internal/ledger/storage_schema.go` (create)
- **Scope**: Write ONLY `marshalRunSnapshot` and `unmarshalRunSnapshot`. No other functions.
- **API**: `func marshalRunSnapshot(snap RunSnapshot) ([]byte, error)` and `func unmarshalRunSnapshot(data []byte) (RunSnapshot, error)`
- **Verification**: `go test -run TestRunSnapshotJSONRoundTrip ./internal/ledger/...` must PASS
- **Timeout**: 60s
- **Context**: `internal/ledger/types.go`, `internal/ledger/storage_test.go`
- **Depends on**: t1

## Wave 2 — Implementation

### t3: [REVIEW] Review t1 + t2
- **Type**: review
- **Scope**: Read the diff. Verify: (1) t1 test actually tests round-trip, (2) t2 marshal+unmarshal are inverse functions, (3) no extra code beyond the signatures.
- **Verification**: Output PASS or REJECT with specific reasons to `evidence/review-t3.md`
- **Timeout**: 60s
- **Depends on**: t2
```

**Scoping rules per task**:
- **Type**: one of `test (RED)`, `production (GREEN)`, `review`, `integration-test`
- **Scope**: explicitly says what the agent IS allowed and IS NOT allowed to change. *"Write ONLY marshalRunSnapshot. No other functions."* This prevents scope creep.
- **Context**: the exact files the agent needs to read. Not the whole repo.

**✅ Positive example** (well-scoped): t2 above. It names one file to create, one file to read, exactly two functions to write, and a verification command.

**❌ Negative example** (over-scoped): *"Implement storage layer"* — no file boundaries, no function list, no limit on lines. The agent could write 500 lines across 5 files. This would be REJECTED at Step 2 validation.

---

### Template: Handoff (`evidence/handoff-<wave>.md`)

After each wave completes, the orchestrator writes a handoff document for the next wave:

```markdown
# Handoff: Wave N → Wave N+1

## Summary
What was accomplished in Wave N (1-2 sentences).

## Files Changed
- `path/to/file.go` — status (CREATED / MODIFIED / UNCHANGED), one-line description

## Verification
- `go build ./...`: PASS / FAIL
- `go test -race ./affected...`: PASS / FAIL (list any failures)
- `go vet ./...`: PASS / FAIL

## Open Questions
- Any decisions deferred to Wave N+1
- Any known design trade-offs

## Next Wave Entry Points
- Exactly what the Wave N+1 sub-agents need to read (file paths + line numbers)
- Context that would be lost if not written down
```

**✅ Positive example**: A handoff that says *"Wave 1 created `storage_schema.go` with marshalRunSnapshot/unmarshalRunSnapshot. Wave 2 should read lines 10-45 of that file to see the payload format before implementing the repository."*

**❌ Negative example**: No handoff at all — just *"Wave 1 done, proceed"* with no context. The Wave 2 sub-agent has to reverse-engineer what Wave 1 did.

---

### Template: Reviewer Output (`evidence/review-<task-id>.md`)

```markdown
# Review: <task-id>

## Reviewed Items
- File(s): `path/to/file.go`
- Task type: test / production / integration

## Checklist
- [ ] Code compiles (`go build ./pkg/...`)
- [ ] Tests pass (`go test -run <test> ./pkg/...`)
- [ ] No files modified outside scope
- [ ] No extra code beyond the assigned API
- [ ] Signatures match the plan (exact names and types)
- [ ] Error paths are handled (not just happy path)
- [ ] No debug code, println, or commented-out code
- [ ] TDD rule followed (test written first)

## Verdict
PASS / REJECT

## If REJECT: Reason
- Specific issue with file:line reference
- What needs to change

## If PASS: Summary
- One-line confirmation that the task meets the checklist
```

---

## Protocol (7 Steps — no skipping, no reordering)

### Step 0 — Plan, Challenge & Lock

**Who**: Orchestrator agent (you).
**Duration cap**: 20 minutes or 3 sub-agent tasks.
**Produces**: `.ai/plan/<name>/plan.md` + `.ai/plan/<name>/evidence/`.

**Actions (in order)**:

1. **Create the plan directory.**
   - `mkdir -p .ai/plan/<name>/evidence .ai/plan/<name>/audit`

2. **Read the codebase first.**
   - Read every relevant file — the interface/contract being implemented, ALL existing implementations, the callers, the tests, the config wiring, and any existing plans under `.ai/plan/`.
   - If touching sensitive packages: read `.ai/invariants.md` and run invariant tests.
   - **Verify no file conflicts**: `ls <proposed-new-file-paths>`.

3. **Build an evidence ledger** → `.ai/plan/<name>/evidence/ledger.md`.
   - Current state of every touched file (what exists, what doesn't, key patterns, relevant signatures).

4. **Write the plan** using the **plan.md template** above. Must contain all sections including scorecard.

5. **Dispatch 2-4 parallel challenge agents.**
   - Prompt: *"You are a hostile reviewer. Find gaps, missing edge cases, wrong assumptions, risky dependencies, missing test scenarios (unit AND integration), or incorrect API choices in this plan. Do NOT confirm — attack. Report each finding with severity (HIGH/MEDIUM/LOW)."*
   - Each agent receives the plan + evidence ledger only.
   - Save outputs to `.ai/plan/<name>/evidence/challenge-<N>.md`.

6. **Disposition every challenge output.**
   - Confirmed gap → update plan. Re-version.
   - Rejected → one-line rationale in disposition log.
   - Competing → orchestrator decides and documents why.
   - Save to `.ai/plan/<name>/evidence/disposition.md`.
   - Re-score scorecard. Any FAIL → plan rejected.

7. **Lock the plan.** No further edits without returning to Step 0.

**Gate**: All challenges dispositioned. Zero unaddressed HIGH. Scorecard all PASS. File-conflict check passed.

---

### Step 1 — Micro-Task Breakdown

**Who**: Orchestrator agent.
**Duration cap**: 10 minutes.
**Produces**: `.ai/plan/<name>/tasks.md` using the **tasks.md template**.

**Rules**:
- **Max 50 lines of new code per task.** If a function or file exceeds this, slice it.
- **Max 1 file per task.** A task creates OR modifies one file, never both.
- **Tests are PRECEDING tasks.** Every production-code task must have a test task preceding it.
- **Reviewer tasks.** After every 2-3 implementation tasks, a reviewer task is scheduled in the next wave.
- **Every task has a Scope section** that explicitly says what IS and IS NOT allowed.
- **Every task has a Context section** listing exact files to read.

**Gate**: All tasks follow the template. Every production task has a preceding test task. Every 2-3 production tasks has a reviewer task. Scope boundaries are explicit.

---

### Step 2 — Validate Each Micro-Task

**Who**: Parallel sub-agents (one per wave).
**Duration cap**: 3 minutes per validator.
**Produces**: `.ai/plan/<name>/validation.md`.

**Actions**:

1. Dispatch 1 validation agent per wave. Prompt: *"You are validating these micro-task definitions against the templates. Read the existing code they interface with. Are scope boundaries clear? Are signatures exact? Is the RED test correctly specified? Output PASS or REJECT per task."*
2. If a task lacks a Scope section, a Context section, or an exact API signature → REJECT automatically.
3. Output appended to `validation.md`.

**Gate**: All tasks PASS. Any REJECT → return to Step 1. Second REJECT on same task → Step 0.

---

### Step 3 — Verify & Finalize

**Who**: Orchestrator agent.
**Duration cap**: 5 minutes.
**Produces**: Locked `tasks.md`.

**Actions**:

1. Read all validation outputs.
2. Adjust task boundaries if needed.
3. **Final idempotency check**: verify no proposed file path already exists (unless "modify").
4. Lock `tasks.md`. Immutable.

**Gate**: Task list locked. Idempotency check passed.

---

### Step 4 — Orchestrate Implementation (TDD)

**Who**: Orchestrator + parallel sub-agents.
**Produces**: Working code + handoff documents.

**Per micro-task (RED → GREEN → REFACTOR):**

1. **RED phase** (test tasks): Sub-agent writes the test file with a failing test. No production code. Saves test failure evidence to `.ai/plan/<name>/evidence/red-<task-id>.log`. Verification: `go test -run TestXxx` FAILS.

2. **GREEN phase** (production tasks): Sub-agent writes the minimal production code that makes the paired RED test pass. Verification: `go test -run TestXxx` PASSES.

3. **REFACTOR phase**: Cleanup only. Tests must still pass.

**Wave execution:**

1. Execute waves **in order**. Never start Wave N until Wave N-1 passes gate.
2. Within a wave, dispatch all tasks in **parallel** if no dependency.
3. **After each wave, orchestrator writes a handoff** → `.ai/plan/<name>/evidence/handoff-<wave>.md` using the handoff template.
4. **Reviewer tasks** in Wave N read the output of Wave N-1 using the reviewer template. Must PASS before Wave N+1 can proceed.
5. Each sub-agent MUST:
   - Read context scope files + `.ai/invariants.md` if sensitive.
   - Follow the template for their task type.
   - Run `go build ./<pkg>/... && go vet ./<pkg>/... && go test -count=1 -run <test> ./<pkg>/...`
   - Fix ALL errors before reporting DONE.
   - **If stuck >2 min**: report BLOCKED with exact error.
   - **Do not modify files outside the scope in their task definition.**
   - **Do not add code beyond the assigned API signatures.**

6. **Orchestrator wave gate check:**
   - `go build ./...` passes.
   - `go test -count=1 -race ./<all-affected-packages>/...` passes.
   - **Cross-wave check**: test ALL packages touched by any wave so far.
   - **Handoff completeness**: verify the handoff document exists and has all sections filled.
   - Wave fails: quick fix (<5 lines) → apply directly. Plan flaw → Step 0.

**Gate**: All waves pass. Every RED produced failing-test evidence. Every GREEN made its test pass. Handoff document exists for each wave.

---

### Step 5 — Bug Audit Loop

**Who**: Orchestrator + hostile sub-agents.
**Duration cap**: 3 rounds default, 5 rounds max.
**Produces**: `.ai/plan/<name>/audit/round-<N>.md`.

**Actions**:

1. Dispatch **3-4 hostile auditors**. Prompt: *"Find bugs, races, data loss, panics, contract violations. Report severity + file:line. Do NOT confirm — attack."*
2. Each auditor reads actual code. Save to `audit/round-<N>-agent-<M>.md`.
3. Per finding:
   - **Confirmed**: fix immediately, run `go test -race ./...`, commit.
   - **Rejected with rationale**: write a targeted test proving the finding is not a bug.
   - **Uncertain**: write a targeted test. If passes → rejected. If fails → confirmed+fixed.
4. Loop until zero bugs OR 5 rounds (→ plan rejected with evidence).
5. **Regression guard**: if fix breaks tests, halt+revert+re-analyse.

**Gate**: All auditors report zero bugs. `go test -race ./...` passes on ALL packages.

---

### Step 6 — Commit & Push

**Who**: Orchestrator agent.
**Duration cap**: 5 minutes.
**Produces**: Commit + push + `.ai/plan/<name>/done`.

**Actions**:

1. **Diff review**: check for debug code, secrets, out-of-scope files, binaries.
2. **TDD audit**: verify every production file has a `_test.go` counterpart. If missing → return to Step 4.
3. **Template compliance**: verify the commit message follows conventional commits format.
4. **Final verification**: `go build ./... && go vet ./... && go test -race ./...`
5. Conventional commit message (`type(scope): subject`, ≤72 chars). Body: what changed, why, verification status, link to `ref: .ai/plan/<name>/`.
6. `git push`.
7. `echo "PASS" > .ai/plan/<name>/done` (not just empty file — encodes verification status).

---

## Fast Path (trivial changes)

If the change is **trivial** (≤5 lines, single file, no new types, no config):
- Skip Steps 0-3. Start at Step 4: implement directly + write test using templates.
- Step 5: dispatch 1 hostile auditor (not 3-4).
- Step 6: normal commit.

**✅ Positive example of trivial**: Renaming a single function from `Foo` to `Bar` in one file, updating the one call site. 3 lines changed.

**❌ Negative example (NOT trivial)**: *"Just adding a field to a struct"* — if that struct is serialized (JSON, TOML), the change affects config loading and backwards compatibility. Full ADLC required.

---

## Rejection & Rollback Rules

| Condition | Action |
|-----------|--------|
| Step 0 challenge reveals fundamental design flaw | Plan **rejected**. Start over. |
| Step 0 scorecard has any FAIL | Plan **rejected**. |
| Step 2 validator REJECTs (missing template section) | Return to Step 1. 2nd REJECT on same task → Step 0. |
| Step 4 RED phase missing (test not written first) | Halt. Task must be redone. |
| Step 4 reviewer REJECTs with checklist evidence | Return task to orchestrator for fixing. |
| Step 4 handoff missing after wave | Do not start next wave. Orchestrator must write handoff. |
| Step 4 sub-agent modifies files outside scope | Revert changes. Re-dispatch with tighter scope. |
| Step 5 audit loop exceeds 5 rounds | Plan **rejected**. Return to Step 0 with evidence. |
| Step 5 fix breaks existing tests | Halt. Revert. Re-analyse. |
| Step 6 missing test for production file | Return to Step 4. Do not commit. |
| Any regression | Halt, revert, Step 0. |

---

## Outcome Examples

### ✅ Positive Outcomes (what to aim for)

| Step | Good Outcome |
|------|-------------|
| **Step 0** | Plan has all 8 sections filled. Scorecard: 7/7 PASS. Challenge agents find only MEDIUM/LOW issues that are quickly dispositioned. |
| **Step 1** | 5-12 micro-tasks across 2-3 waves. Every production task has a paired test task. Reviewer tasks after every 2-3 production tasks. Scope boundaries clear. |
| **Step 2** | All validators PASS. "Task t3 scope is well-defined — it knows exactly which file to create and which functions to write." |
| **Step 3** | Idempotency check: no file conflicts. Task list locked. |
| **Step 4** | Every RED phase produces a failing test with clear expected error. Every GREEN makes its test pass. Reviewer tasks PASS. Handoffs are complete. Wave gates pass. |
| **Step 5** | Round 1 finds 2 MEDIUM bugs. Fixed in 2 commits. Round 2: ZERO BUGS. `go test -race ./...` all pass. |
| **Step 6** | Commit message follows `type(scope): subject`. Body references plan. `echo "PASS" > .ai/plan/<name>/done`. |

### ❌ Negative Outcomes (rejection triggers)

| Step | Bad Outcome | What Happens Next |
|------|-------------|-------------------|
| **Step 0** | Plan has no API surface section. Scorecard shows 3 FAIL. | Plan REJECTED. Return to Step 0 with challenge evidence. |
| **Step 1** | Task t3 is "implement storage layer" — no file path, no function signature, no scope boundary. | Step 2 validator REJECTs. Return to Step 1. If same task REJECTs again → Step 0. |
| **Step 3** | Proposed file `internal/ledger/storage.go` already exists from a previous cycle. | Idempotency check FAILS. Task corrected to "modify" instead of "create". |
| **Step 4** | Sub-agent modifies `internal/ledger/types.go` (adding fields) even though scope said "storage_schema.go only". | Changes reverted. Sub-agent re-dispatched with explicit warning. |
| **Step 4** | No RED phase evidence saved. Sub-agent wrote test + production code in one shot, violating TDD. | Task halted. Must be redone with test first. |
| **Step 4** | Reviewer REJECTs because "code uses println() for debugging". | Returned to orchestrator. Debug code removed. Reviewer re-checks. |
| **Step 5** | Round 4 still finds a race condition. Round 5 finds another. | Plan REJECTED. Return to Step 0 with all 5 rounds of evidence. |
| **Step 6** | New file `storage.go` has no `storage_test.go`. | Commit blocked. Return to Step 4. |

---

## Invariant Enforcement

Before Step 0 and Step 4, read `.ai/invariants.md` + run invariant tests if touching:
- `internal/cli/`, `internal/tools/`, `internal/agent/`, `internal/chat/`, `internal/config/`
- `internal/ledger/`, `internal/coordinator/`, `internal/events/`, `internal/storage/`

If invariant fails → blocked until restored or manifest updated with `Invariant-Update: INV-XX <reason>`.

---

## Escalation Protocol

Sub-agent BLOCKED >2 min:
1. Orchestrator reads the blocker.
2. Missing file/type → create it, unblock, continue.
3. Conceptual blocker → cancel agent, implement self, or Step 0.
4. Blocker reveals plan flaw → cancel all, Step 0.

---

## Artifact Chain

```
Step 0 → .ai/plan/<name>/plan.md                           (locked plan + scorecard)
         .ai/plan/<name>/evidence/ledger.md               (evidence ledger)
         .ai/plan/<name>/evidence/challenge-<N>.md         (challenge outputs)
         .ai/plan/<name>/evidence/disposition.md           (disposition log)
Step 1 → .ai/plan/<name>/tasks.md                          (micro-task breakdown)
Step 2 → .ai/plan/<name>/validation.md                    (PASS/REJECT per wave)
Step 3 → tasks.md locked                                   (no further edits)
Step 4 → .ai/plan/<name>/evidence/red-<task-id>.log        (RED phase evidence)
         .ai/plan/<name>/evidence/handoff-<wave>.md        (wave handoffs)
         .ai/plan/<name>/evidence/review-<task-id>.md      (reviewer checklists)
         working code in tree                                (GREEN phase)
Step 5 → .ai/plan/<name>/audit/round-<N>.md               (audit round findings)
Step 6 → git commit + push                                  (.ai/plan/<name>/done)
```
