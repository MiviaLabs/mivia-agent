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
| **Integration test** | Step 4c, after all micro-tasks in a wave | Orchestrator or dedicated sub-agent | End-to-end path through all layers (e.g., StorageLedgerRepository → Coordinator → CLI tool). Tests the contract. | `go test -run TestIntegration ./...` passes |
| **Invariant test** | Before Step 0 + Step 4 | Orchestrator | Existing invariant manifest. Ensures no regression in sensitive packages. | `make invariants` passes |
| **Regression test** | Step 5 (bug audit) | Hostile auditor | Proves a reported bug does NOT exist (false positive guard). | Test passes → rejected. Test fails → confirmed bug. |
| **Race test** | After every wave + Step 6 | Orchestrator | Detects data races under `-race`. | `go test -race ./affected...` passes |

**TDD Rule**: Every micro-task of type "production code" must be paired with a preceding or same-micro-task "test" that validates the new API. The test is written FIRST (RED phase), then the production code is written to make it pass (GREEN phase). No production code is committed without its test.

---

## Artifact Directory

All plans, task breakdowns, validation logs, and evidence lives under `.ai/plan/<name>/`.

```
.ai/plan/<name>/
├── plan.md              # Plan + scorecard (final locked version)
├── tasks.md             # Task breakdown (waves, task specs)
├── validation.md        # Validation results per wave
├── evidence/            # Evidence ledger, challenge outputs, disposition log
│   ├── ledger.md
│   ├── challenge-01.md
│   └── challenge-02.md
├── audit/               # Bug audit rounds
│   ├── round-01.md
│   └── round-02.md
└── done                 # Empty file — created on successful Step 6
```

---

## Protocol (7 Steps — no skipping, no reordering)

### Step 0 — Plan, Challenge & Lock

**Who**: Orchestrator agent (you).
**Duration cap**: 20 minutes or 3 sub-agent tasks, whichever comes first.
**Produces**: `.ai/plan/<name>/plan.md` + `.ai/plan/<name>/evidence/`.

**Actions (in order)**:

1. **Create the plan directory.**
   - `mkdir -p .ai/plan/<name>/evidence .ai/plan/<name>/audit`

2. **Read the codebase first.**
   - Read every relevant file — the interface/contract being implemented, ALL existing implementations, the callers, the tests, the config wiring, and any existing plans under `.ai/plan/`.
   - If touching sensitive packages: read `.ai/invariants.md` and run invariant tests.
   - **Verify no file conflicts**: `ls <proposed-new-file-paths>` to ensure you're not overwriting something that exists.

3. **Build an evidence ledger** → save to `.ai/plan/<name>/evidence/ledger.md`.
   - Current state of every touched file (what exists, what doesn't, key patterns, relevant signatures).

4. **Write the plan** → save to `.ai/plan/<name>/plan.md`. Sections:

   | Section | Required Content |
   |---------|-----------------|
   | **Goal** | One sentence. |
   | **Files to create** | Exact paths. |
   | **Files to modify** | Exact paths + one-line summary per change. |
   | **API surface** | Exact Go function/type signatures for new public/exported code. |
   | **Dependency graph** | Wave 1 → Wave 2 → … |
   | **Test strategy** | For each new API: exact test function name, whether unit or integration, what scenario it covers, and expected RED-phase failure. |
   | **Plan scorecard** | 6 PASS/FAIL criteria (see below). |
   | **Rollback criterion** | What condition kills this plan. |

   **Plan scorecard:**
   1. All existing tests will still pass (verified by understanding the change)
   2. No new import cycles
   3. No breaking changes to existing public API
   4. New code is testable in isolation (no global state, no required network)
   5. Config changes are backward-compatible
   6. Every new public function has at least one named test scenario in the test strategy
   7. Integration test path is identified (which end-to-end scenario validates this work)

5. **Dispatch 2-4 parallel challenge agents.**
   - Prompt: *"You are a hostile reviewer. Find gaps, missing edge cases, wrong assumptions, risky dependencies, missing test scenarios (unit AND integration), or incorrect API choices in this plan. Do NOT confirm — attack. Report each finding with severity (HIGH/MEDIUM/LOW)."*
   - Each agent receives the plan + evidence ledger only.
   - Save outputs to `.ai/plan/<name>/evidence/challenge-<N>.md`.

6. **Disposition every challenge output.**
   - Confirmed gap → update plan. Re-version.
   - Rejected → one-line rationale in disposition log.
   - Competing → orchestrator decides and documents why.
   - Save disposition to `.ai/plan/<name>/evidence/disposition.md`.
   - Re-score scorecard. Any FAIL → plan rejected.

7. **Lock the plan.**
   - No further edits without returning to Step 0.

**Gate**: All challenges dispositioned. Zero unaddressed HIGH. Scorecard all PASS. File-conflict check passed.

---

### Step 1 — Micro-Task Breakdown

**Who**: Orchestrator agent.
**Duration cap**: 10 minutes.
**Produces**: `.ai/plan/<name>/tasks.md`.

**Rules**:
- **Max 50 lines of new code per task.** If a function or file exceeds this, it's sliced into multiple tasks.
- **Max 1 file per task.** A task creates OR modifies one file, never both. If a change spans multiple files, each file is its own task.
- **Tests are PRECEDING tasks.** For every production-code task, there MUST be a test task that precedes it in the same wave (or immediately before it). The test task writes the RED test. The production task writes the GREEN implementation. They must be in the same wave or consecutive waves.
- **Reviewer tasks:** After every 2-3 implementation tasks, a reviewer task is scheduled in the NEXT wave. The reviewer reads the previous sub-agent's code and validates correctness. This catches semantic mismatches.

**Task spec format:**

```
t3: Create RunSnapshot JSON marshal/unmarshal
  Wave: 1
  File: internal/ledger/storage_schema.go (create)
  Type: production
  API: marshalRunSnapshot(RunSnapshot) ([]byte, error)
       unmarshalRunSnapshot([]byte) (RunSnapshot, error)
  Depends on: t2 (RED test for these functions)
  Verification: go build ./internal/ledger/... && go test -run TestMarshalRunSnapshot ./internal/ledger/...
  Timeout: 60s
  Context: internal/ledger/types.go
```

```
t4: Review t2 + t3
  Wave: 2
  File: review of internal/ledger/storage_schema.go
  Type: review
  Depends on: t3
  Verification: read the output of t2 and t3, verify signatures match,
                 verify round-trip (marshal→unmarshal→compare)
  Timeout: 60s
```

**Wave dependency rules:**
- Wave 1: test tasks (RED tests for the bottom of the dependency graph)
- Wave 2: implementation tasks (GREEN implementations for Wave 1 tests) + reviewer tasks for Wave 1
- Wave 3: integration tests + reviewer tasks for Wave 2
- Subsequent waves: repeat test → implement → review for each layer

**Gate**: Every production task has a preceding test task. Every 2-3 production tasks has a reviewer task in the next wave. No task exceeds 50 lines or 1 file.

---

### Step 2 — Validate Each Micro-Task

**Who**: Parallel sub-agents (one per wave).
**Duration cap**: 3 minutes per validator (micro-tasks are small).
**Produces**: `.ai/plan/<name>/validation.md`.

**Actions**:

1. Dispatch 1 validation agent per wave. Prompt: *"You are validating these micro-task definitions. Read the existing code they interface with. Can each task be implemented as described? Are the test tasks correctly specified (expected RED failure)? Are the boundaries correct (max 50 lines, 1 file)? Output PASS or REJECT per task with specific reasons."*
2. The validator reads only the context scope files — not the whole repo.
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
**Produces**: Working code in tree.

**Sub-steps per micro-task:**

Each micro-task goes through RED → GREEN → REFACTOR:

1. **RED phase** (test tasks only): Sub-agent writes the test file with a failing test that targets the API surface defined in the plan. No production code. Verification: `go test -run TestXxx ./pkg/...` FAILS with the expected error (e.g., "undefined function" or custom assertion). The sub-agent saves the test output as evidence.

2. **GREEN phase** (production tasks only): Sub-agent writes the minimal production code that makes the paired RED test pass. No extra code. Verification: `go test -run TestXxx ./pkg/...` PASSES. The test was already written in the RED phase.

3. **REFACTOR phase** (same agent or reviewer): If the implementation needs cleanup (naming, comments, error handling), the sub-agent does it here. Tests must still pass.

**Wave execution:**

1. Execute waves **in order** (Wave 1 → Wave 2 → …). Never start Wave N until Wave N-1 passes gate.
2. Within a wave, dispatch all tasks in **parallel** if they have no dependency between them.
3. **Reviewer tasks** in wave N read the output of wave N-1 and must PASS before wave N can proceed. If a reviewer REJECTs, the implementation task is sent back to the orchestrator for fixing before the wave advances.
4. Each sub-agent MUST:
   - Read context scope files + `.ai/invariants.md` if sensitive.
   - Write test or production code (not both in one task — TDD rule).
   - Run `go build ./<pkg>/... && go vet ./<pkg>/... && go test -count=1 -run <test> ./<pkg>/...`
   - Fix ALL errors before reporting DONE.
   - **If stuck >2 min**: report BLOCKED with exact error.
   - **Do not modify files outside the assigned path list.**
   - **RED phase outputs**: save test failure evidence to `evidence/red-<task-id>.log`.

5. **Orchestrator wave gate check:**
   - `go build ./...` passes.
   - `go test -count=1 -race ./<all-affected-packages>/...` passes.
   - **Integration test run** (if this wave has integration tests): `go test -run TestIntegration ./<pkg>/...` passes.
   - **Cross-wave check**: test ALL packages touched by any wave so far.
   - Wave fails: quick fix (<5 lines) → apply directly. Plan flaw → Step 0.

**Gate**: All waves pass. Every RED phase produced failing test evidence. Every GREEN phase made its test pass. Integration tests pass after all waves.

---

### Step 5 — Bug Audit Loop

**Who**: Orchestrator + hostile sub-agents.
**Duration cap**: 3 rounds default, 5 rounds max.
**Produces**: `.ai/plan/<name>/audit/round-<N>.md`.

**Actions**:

1. Dispatch **3-4 hostile auditors**. Prompt: *"Find bugs, races, data loss, panics, contract violations. Report severity + file:line. Do NOT confirm — attack."*
2. Each auditor reads actual code files. Save to `audit/round-<N>-agent-<M>.md`.
3. Per finding:
   - **Confirmed**: fix immediately, run `go test -race ./...`, commit.
   - **Rejected with rationale**: write a targeted test proving the finding is not a bug. Commit the test.
   - **Uncertain**: write a targeted test. If passes → rejected. If fails → confirmed+fixed.
4. Loop until zero bugs OR 5 rounds (→ plan rejected with evidence).
5. **Regression guard**: if a fix breaks existing tests, halt+revert+re-analyse.
6. **Blast-radius**: audit only files changed THIS cycle.

**Gate**: All auditors report zero bugs. `go test -race ./...` passes on ALL packages.

---

### Step 6 — Commit & Push

**Who**: Orchestrator agent.
**Duration cap**: 5 minutes.
**Produces**: Commit + push + `.ai/plan/<name>/done`.

**Actions**:

1. **Diff review**: check for debug code, secrets, out-of-scope files, binaries.
2. **Final verification**: `go build ./... && go vet ./... && go test -race ./...`
3. **TDD audit**: verify that every new file has a corresponding `_test.go` file. If any production file is missing tests, do not commit — return to Step 4.
4. Conventional commit message (`type(scope): subject`, ≤72 chars).
5. Body: what changed, why, verification status, link to plan directory.
6. `git push`.
7. `touch .ai/plan/<name>/done`.

**Gate**: Push succeeds. Tree clean. Diff review passed. Every production file has its test.

---

## Fast Path (trivial changes)

If the change is **trivial** (≤5 lines, single file, no new types, no config):
- Skip Steps 0-3. Start at Step 4: implement directly + write test.
- Step 5: dispatch 1 hostile auditor (not 3-4).
- Step 6: normal commit.

**Gate**: "Trivial" = single file, ≤5 lines added/modified, no new types/functions, no config, no test file creation. If unsure, run full ADLC.

---

## Rejection & Rollback Rules

| Condition | Action |
|-----------|--------|
| Step 0 challenge reveals fundamental design flaw | Plan **rejected**. Start over. |
| Step 0 scorecard has any FAIL | Plan **rejected**. |
| Step 2 validator REJECTs | Return to Step 1. 2nd REJECT on same task → Step 0. |
| Step 4 RED phase missing (test not written first) | Halt. Task must be redone with test first. |
| Step 4 reviewer REJECTs | Return implementation task to orchestrator for fixing. |
| Step 4 wave fails — plan flaw | Return to Step 0. |
| Step 4 wave fails — quick fix | Apply directly (<5 lines), re-verify, proceed. |
| Step 5 audit loop exceeds 5 rounds | Plan **rejected**. Return to Step 0 with evidence. |
| Step 5 fix breaks existing tests | Halt. Revert. Re-analyse. |
| Step 6 missing test for production file | Return to Step 4. Do not commit. |
| Any regression | Halt, revert, Step 0. |

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
Step 0 → .ai/plan/<name>/plan.md         (locked plan + scorecard)
         .ai/plan/<name>/evidence/        (ledger, challenges, disposition)
Step 1 → .ai/plan/<name>/tasks.md        (micro-task breakdown)
Step 2 → .ai/plan/<name>/validation.md   (PASS/REJECT per wave)
Step 3 → tasks.md immutable              (no further edits)
Step 4a→ evidence/red-<id>.log           (RED phase: failing test output)
Step 4b→ working code in tree            (GREEN phase: passing tests)
Step 4c→ working integration tests       (all waves done)
Step 5 → .ai/plan/<name>/audit/          (rounds, findings, fixes)
Step 6 → git commit + push               (.ai/plan/<name>/done)
```
