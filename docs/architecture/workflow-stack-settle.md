# Stack settle

Multi-chunk stacking workflows deliver code changes in ordered waves of pull requests. A plan run transitions to `delivery_pending` upon completing its execution phase, then drives its chunk stack to completion before settling.

## Stack drive lifecycle

Stack driving executes in five stages:

1. **Plan run execution**: The plan run finishes its step graph and enters the `delivery_pending` status (`internal/workflows/localengine/engine_stack.go:47`).
2. **Ledger initialization**: The driver seeds chunk tasks into the stack ledger (`internal/workflows/delivery/stacking_plan.go`, `internal/workflows/localengine/engine_stack.go:92`).
3. **Chunk execution and delivery**: Chunk runs execute in topological order (`delivery.TopologicalOrder`). Under `merge_policy = "auto"`, chunks deliver automatically and monitor pull requests for merge completion. Under `merge_policy = "approve"`, chunks transition to `reviewed` and pause for per-chunk delivery grants.
4. **Integration run**: After all chunk pull requests merge, the driver admits the final full-suite integration run (`internal/workflows/localengine/engine_stack_settle.go:28`).
5. **Plan run settlement**: Once the integration run completes and merges, the plan run settles. When `delivery.deliver_plan_run = false` (the default), the plan run settles as `succeeded` without creating a redundant plan pull request (`internal/cliworkflow/workflow_run.go:206`, `internal/workflows/localengine/engine_stack_settle.go:161`). When `delivery.deliver_plan_run = true`, the plan run remains at `delivery_pending` for explicit publication via `mivia workflow deliver`.

## Reconcile and drive entry points

Stack driving runs across four operational paths:

- **CLI foreground execution**: `ExecuteWorkflowRun` drives settled stacks via `maybeDriveSettledStack` before delivery (`internal/cliworkflow/workflow_run.go:173`).
- **CLI foreground resume**: `ExecuteWorkflowResume` drives stacks during resume before publication (`internal/cliworkflow/workflow_resume.go:204`).
- **Session engine hook**: The session workflow engine runs `maybeDriveSettledStack` inside its auto-delivery repair loop (`internal/cliworkflow/workflow_tool_engine.go:314`).
- **Session recovery sweep**: The periodic engine recovery sweep scans parked runs and advances incomplete stacks through `driveParkedStackIfNeeded` (`internal/cliworkflow/workflow_tool_engine_reconcile.go:231, 343, 368`).
- **Local engine drive loop**: The standalone engine advances stacks through `stackDriveAfterPark` and `driveStackLoop` (`internal/workflows/localengine/engine_stack.go:47, 139`).

## Failure propagation

When a chunk fails terminally (`delivery.StatusFailed`) or cancels (`delivery.StatusCanceled`), the stack halts progress (`internal/workflows/localengine/engine_stack_settle.go:200`).

The failure propagates through these mechanisms:

- **Failure classification**: `StackPlanRunFailureReasonImpl` inspects the task ledger and integration run status to detect terminal failures (`internal/cliworkflow/gate_impl.go:40`).
- **Automated settlement**: The CLI run path and session recovery sweep fail-settle the plan run to `failed` using `settleStackPlanRunFailed` (`internal/cliworkflow/workflow_stack_settle.go:17, 65`, `internal/cliworkflow/workflow_run.go:183`, `internal/cliworkflow/workflow_tool_engine_reconcile.go:243, 275`).
- **Delivery refusal**: `RefuseFailedStackPlanRunDelivery` rejects `mivia workflow deliver` on failed stacks and CAS-transitions the plan run to `failed` (`internal/cliworkflow/workflow_stack_settle.go:35`).

## Lock behavior

Workflow execution uses file-backed flock coordination to serialize execution and settlement across processes (`internal/cliworkflow/workflow_resume_lock.go:38`).

- **Lock timeout**: `WorkflowResolutionLockWait` bounds the lock acquisition wait to 60 seconds (`internal/cliworkflow/workflow_tool_engine.go:38`).
- **Retry backoff**: Lock acquisition retries use exponential backoff with full jitter to avoid stampedes (`internal/cliworkflow/workflow_resume_lock.go:73`, tested in `internal/cliworkflow/workflow_resume_lock_findings_test.go:44`).
- **Error naming**: `LockWorkflowExecutionFile` wraps low-level file locks and applies `renameGitExcludeLockError` (`internal/cliworkflow/workflow_resume_lock.go:54`) to report errors under `lock workflow execution:` instead of Git exclude prefixes (`internal/cliworkflow/workflow_resume_lock_findings_test.go:20`).

## Known limitations

- **Autonomous background polling requires an active process**: The recovery sweep requires a running session engine or foreground CLI command. When no `mivia` process runs, out-of-band pull request merges do not settle the plan run until an operator runs `mivia stack drive`, `mivia workflow resume`, or starts a session.
- **Local engine drive halt**: The local engine (`internal/workflows/localengine/engine_stack.go:139`) stops driving when a chunk fails, leaving the stack resumable. Automated CAS failure settlement of the plan run is performed by the CLI and reconciliation layers (`internal/cliworkflow/workflow_stack_settle.go:17`).

## See also

- [Subagent orchestration](overview.md#subagent-orchestration)
- [Workflow architecture](workflows.md)
- [Embedded persistence](embedded-persistence.md)
