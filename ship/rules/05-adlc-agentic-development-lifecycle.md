# ADLC — Agentic Development Lifecycle (Shipped Edition)

**⚠️ THIS IS THE MANDATORY PROCESS FOR ALL WORK.**
Read this file before starting any task.

**Scope**: All feature work, bug fixes, refactors, and cross-package changes.

**Storage model**: Zero files for workflow artifacts. Everything lives in the orchestrator's context or tool results.

**Fast Path**: Trivial changes (≤5 lines, single file, no new types) may skip Steps 0-3.

---

## Principles

1. **TDD — tests before code.** RED (failing assertion) → GREEN (passing code).
2. **Micro-tasks.** 1 function OR 1 file per task. Fresh context per task.
3. **Challenge before build.** Every plan is attacked before code is written.
4. **Test-drive the bug audit.** When uncertain, write a test first.
5. **Fail fast, roll back.** If a step reveals a plan flaw, return to Step 0.
6. **Zero files for workflow.** No plan files, no artifact directories.

---

## Protocol (7 Steps)

### Step 0 — Plan, Challenge & Lock
- Read existing code. Build plan in context (no files).
- Dispatch 2-4 hostile challenge agents via `dispatch_tasks`.
- Disposition findings in context. Lock plan.

### Step 1 — Micro-Task Breakdown
- 1 file per task, 1 function per production task.
- Test tasks precede production tasks.
- Reviewer every 2-3 impl tasks.

### Step 2 — Validate Each Task
- Validate via `dispatch_tasks`. PASS or REJECT per task.

### Step 3 — Verify & Finalize
- Lock task list in context. No further changes without Step 0.

### Step 4 — Implement (TDD)
- RED phase: write failing test first.
- GREEN phase: write minimal code to pass test.
- Execute waves in order via `spawn_agent` / `dispatch_tasks`.

### Step 5 — Bug Audit Loop
- Dispatch hostile auditors via `dispatch_tasks`.
- Loop until zero bugs or 5 rounds max.

### Step 6 — Commit & Push
- Diff review. Final verification. Conventional commit. Push.

---

## Tool Reference

| Step | Tool | Usage |
|------|------|-------|
| Step 0 challenge | `dispatch_tasks` | Per-agent, `handler: "multi_step"` |
| Step 4 waves | `spawn_agent` (seq) / `dispatch_tasks` (parallel) | `wait: "run"` for waves |
| Step 4 stuck | `inspect_agents` → `cancel_run` | Check → abort |
| Step 5 audit | `dispatch_tasks` | 3-4 agents, `partial_results: true` |
| Step 5 fix | `delegate` | Single focused task |
| Step 6 verify | Direct execution | `build + test -race` |
