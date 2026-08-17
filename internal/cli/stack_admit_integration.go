package cli

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/stacking"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// integrationRunInputs builds the admission inputs for the final full-suite
// integration run (see stacking.IntegrationRunInputs).
func integrationRunInputs(planInputs map[string]string, prBase string) (map[string]any, map[string]string) {
	return stacking.IntegrationRunInputs(planInputs, prBase)
}

// stackDecomposedChunks reports whether runID is the plan run of a
// multi-chunk stack (see stacking.DecomposedChunks). ok=false covers every
// other case (not a stacking plan run, single/no_bug, a malformed decompose
// output, or a lookup failure) - callers must treat a lookup failure as "not
// applicable", never as a refusal or a false "undriven" diagnostic.
func stackDecomposedChunks(ctx context.Context, repo workflowledger.Repository, runID string) (chunks int, ok bool) {
	return stacking.DecomposedChunks(ctx, repo, runID)
}

// stackPlanRunGate classifies a run for the drive-before-delivery decision
// `mivia workflow deliver`/`mivia stack drive` must make before settling or
// publishing a delivery_pending run's own result.
type stackPlanRunGate int

const (
	// stackPlanRunNotApplicable: not the plan run of a multi-chunk stack (a
	// non-stacking run, a single/no_bug plan, or a chunk/integration run) -
	// proceed with normal delivery.
	stackPlanRunNotApplicable stackPlanRunGate = iota
	// stackPlanRunIncomplete: a multi-chunk plan run whose stack has not yet
	// driven to completion (stackDriveCompleted false) - must be refused.
	stackPlanRunIncomplete
	// stackPlanRunComplete: a multi-chunk plan run whose stack drove to
	// completion (every chunk merged, integration run settled) - safe to
	// settle or deliver.
	stackPlanRunComplete
	// stackPlanRunFailed: a multi-chunk plan run whose stack has terminally
	// failed (a chunk task reached stackStatusFailed, or the integration run
	// settled to a terminal failure status). Callers can fail-settle the plan
	// run instead of refusing it forever as merely incomplete.
	stackPlanRunFailed
)

// classifyStackPlanRunDelivery reports how runID relates to a multi-chunk
// stack for delivery purposes. `workflow run`/`resume` drive such a stack's
// chunks BEFORE delivering the plan run itself (drive-before-delivery
// ordering, see maybeDriveSettledStack's call sites); delivering the plan
// run directly, without checking whether the stack actually finished,
// either abandons an undriven stack while reporting the plan run
// "succeeded" (live finding, 2026-08-15), or - the companion bug - refuses
// forever a stack that already finished driving, since every multi-chunk
// plan run keeps its succeeded decompose attempt whether or not the stack
// it produced ever completed (live finding, 2026-08-16). Completion reuses
// the same durable check the driver and the session recovery sweep apply
// (stackDriveCompleted), so a CLI operator sees the identical verdict.
// remoteMergeOracle is stackDriveCompleted's parameter: settle paths pass
// true, read-only display surfaces pass false.
func classifyStackPlanRunDelivery(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string, remoteMergeOracle bool) stackPlanRunGate {
	if _, ok := stackDecomposedChunks(ctx, repo, runID); !ok {
		return stackPlanRunNotApplicable
	}
	if failed, _ := stackPlanRunFailureReason(ctx, root, store, repo, runID); failed {
		return stackPlanRunFailed
	}
	policy := stackPlanMergePolicy(ctx, repo, runID)
	if !stackDriveCompleted(ctx, root, store, repo, runID, policy, remoteMergeOracle) {
		return stackPlanRunIncomplete
	}
	return stackPlanRunComplete
}

// stackPlanRunFailureReason reports whether a multi-chunk stack plan run has
// already reached a terminal failure state. A durably failed chunk task, or
// an integration run that settled to a terminal failure status, means the
// stack cannot complete and the plan run should be fail-settled rather than
// refused as merely incomplete.
func stackPlanRunFailureReason(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string) (failed bool, reason string) {
	byID, err := stackTaskMap(tasks.NewStore(store), runID)
	if err == nil {
		for taskID, task := range byID {
			if task.Status == stackStatusFailed {
				return true, fmt.Sprintf("chunk %s failed terminally", taskID)
			}
		}
	}
	intRun, found, err := stackRunRef(repo, runID, stackIntegrationChunkID)
	if err == nil && found {
		switch intRun.Status {
		case workflowledger.RunStatusFailed, workflowledger.RunStatusCanceled,
			workflowledger.RunStatusTimedOut, workflowledger.RunStatusDeliveryFailed:
			return true, fmt.Sprintf("integration run %s is %s", intRun.RunID, intRun.Status)
		}
	}
	return false, ""
}

// errUndrivenStackPlanRun builds the refusal for an incomplete stack's plan
// run: the operator must finish driving it before its own delivery can
// settle or publish. The advice names `mivia stack drive` only: `workflow
// resume` refuses delivery_pending runs, and `workflow run` mints a NEW
// plan run (a second stack) instead of driving the parked one - both are
// dead ends for this run.
func errUndrivenStackPlanRun(runID string) error {
	return fmt.Errorf("workflow run %q is the plan run of a stack that has not fully driven yet: finish it with `mivia stack drive <workflow> --stack %s`, then settle the plan run with `mivia workflow deliver %s` - delivering it now would abandon the undriven stack while reporting the plan run succeeded", runID, runID, runID)
}

// errFailedStackPlanRun builds the refusal for a terminally failed stack's
// plan run. The stack cannot complete, so the operator must inspect or clean
// it up instead of driving it forever.
func errFailedStackPlanRun(runID, reason string) error {
	return fmt.Errorf("workflow run %q is the plan run of a stack that cannot complete: %s - use `mivia stack drive <workflow> --stack %s` to inspect, or delete the run if the failure is unrecoverable", runID, reason, runID)
}
