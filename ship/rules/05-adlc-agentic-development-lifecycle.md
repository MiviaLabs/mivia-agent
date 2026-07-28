# ADLC — Agentic Development Lifecycle (Shipped Edition)

**⚠️ THIS IS THE MANDATORY PROCESS FOR ALL WORK.**
Read this file before starting any task.

**Scope**: All feature work, bug fixes, refactors, and cross-package changes.

**Fast Path**: Trivial changes (≤5 lines, single file, no new types) may skip Steps 0-3. If unsure, use the full ADLC.

---

## Principles

1. **TDD — tests before code.** Write the test first (RED phase: compile + fail assertion), then minimal production code (GREEN phase).
2. **Micro-tasks.** Each task is 1 function OR 1 file, not both. Fresh context per task.
3. **Challenge before build.** Every plan is attacked before any code is written.
4. **Test-drive the bug audit.** When uncertain about a bug report, write a test first.
5. **Fail fast, roll back.** If a step reveals a plan flaw, return to Step 0.
6. **Idempotency.** Every step must be safely re-runnable.

---

## Protocol (7 Steps)

### Step 0 — Plan, Challenge & Lock
- Read existing code first. Build an evidence ledger.
- Write a plan with: goal, files to create/modify, API surface, dependency graph, test strategy, scorecard (all PASS to proceed).
- Dispatch 2-4 hostile challenge agents to attack the plan.
- Disposition all findings. Fix confirmed gaps. Lock the plan.

### Step 1 — Micro-Task Breakdown
- Slice into ≤1 file per task, ≤1 function per production task.
- Declare waves: Wave N depends on Wave N-1.
- Test tasks precede production tasks.

### Step 2 — Validate Each Task
- Validate via sub-agents. PASS or REJECT per task.

### Step 3 — Verify & Finalize
- Lock the task list. No further changes without returning to Step 0.

### Step 4 — Implement (TDD)
- RED phase: write failing test first.
- GREEN phase: write minimal code to pass test.
- Execute waves in order. Run build + test + race after each wave.

### Step 5 — Bug Audit Loop
- Dispatch hostile auditors. Loop until zero bugs or 5 rounds max.
- For each finding: confirmed → fix. Rejected → write test as proof.

### Step 6 — Commit & Push
- Diff review. Final verification. Conventional commit. Push.

---

## Tool Reference

| Step | Tool | Usage |
|------|------|-------|
| Step 0 challenge | `dispatch_tasks` | One task per agent, `handler: "multi_step"` |
| Step 4 implement | `spawn_agent` (waves) / `dispatch_tasks` (parallel) | Sequential waves use `spawn_agent` |
| Step 5 audit | `dispatch_tasks` | 3-4 agents, `partial_results: true` |

---

## Templates & Artifacts

```
.ai/plan/<name>/
├── plan.md              # Locked plan
├── tasks.md             # Micro-task breakdown
├── validation.md        # Validation results
├── evidence/            # Evidence ledger, challenges
├── audit/               # Bug audit rounds
└── done                 # Completion marker
```
