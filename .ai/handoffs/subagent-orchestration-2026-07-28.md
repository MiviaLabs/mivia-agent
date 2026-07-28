# Subagent Orchestration Handoff — 2026-07-28

## Outcome

The orchestration runtime now has repository-backed run state, bounded lifecycle operations, recovered-run handling, session-scoped handles, bounded model-facing errors, and enforced task/budget limits.

## Implemented

- Dispatcher close hooks are idempotent and race-safe.
- Duplicate invocation waiters cannot clear the owning active invocation.
- Failure envelopes use bounded references instead of raw provider/tool/error/prompt bodies.
- Idempotency keys persist request fingerprints and reject mismatched requests.
- Cancellation respects caller deadlines while durable reconciliation continues safely.
- Recovered runs fail closed when no live executor exists; queued recovered tasks can be reconciled and recovered joins expose persisted task results.
- Recovered-handle bookkeeping follows retention lifecycle.
- Orchestration handles are scoped to the originating dispatcher/session and repository.
- Negative budgets and oversized/deep DAGs are rejected before accounting/execution.

## Verification

- Focused package tests: passed.
- Race tests for touched packages: passed.
- `make verify`: passed.
- Semgrep: 0 findings.
- Commit hooks: passed on prior commits.

## Commits

- `26c6e71` — close orchestration lifecycle gaps.
- `e8302ad` — harden recovered orchestration control.
- This handoff captures the follow-up audited fixes pending in the current worktree.

## Remaining risks

- The default repository is intentionally in-memory; process-restart durability requires an injected durable `LedgerRepository`.
- `docs/architecture/other.md` remains a pre-existing docs ownership warning.
- WSL/UNC mode-only changes and the nested `.claude/worktrees/codebase-assessment-refactor-59330b` worktree are unrelated and must remain uncommitted.

## Next action

Review the combined diff, commit the implementation and this handoff, then push the current branch. Follow up with durable-repository integration and end-to-end restart/cancellation tests before production use.
