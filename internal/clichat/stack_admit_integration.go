package clichat

import (
	"context"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// integrationRunInputs builds the admission inputs for the final full-suite
// integration run (see delivery.IntegrationRunInputs).
func integrationRunInputs(planInputs map[string]string, prBase string) (map[string]any, map[string]string) {
	return delivery.IntegrationRunInputs(planInputs, prBase)
}

// stackDecomposedChunks reports whether runID is the plan run of a
// multi-chunk stack (see delivery.DecomposedChunks). ok=false covers every
// other case (not a stacking plan run, single/no_bug, a malformed decompose
// output, or a lookup failure) - callers must treat a lookup failure as "not
// applicable", never as a refusal or a false "undriven" diagnostic.
func stackDecomposedChunks(ctx context.Context, repo workflowledger.Repository, runID string) (chunks int, ok bool) {
	return delivery.DecomposedChunks(ctx, repo, runID)
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
	// driven to completion (cliworkflow.StackDriveCompleted false) - must be refused.
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
// (cliworkflow.StackDriveCompleted), so a CLI operator sees the identical verdict.
// remoteMergeOracle is cliworkflow.StackDriveCompleted's parameter: settle paths pass
// true, read-only display surfaces pass false.
func classifyStackPlanRunDelivery(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string, remoteMergeOracle bool) stackPlanRunGate {
	// An integration run (<stack>:integration) also records decompose chunks
	// when it re-plans mode=multi; it is never a stack plan run itself.
	if snap, err := repo.GetRun(ctx, runID); err == nil && snap.InvocationKey != "" && strings.HasSuffix(snap.InvocationKey, ":"+stackIntegrationChunkID) {
		return stackPlanRunNotApplicable
	}
	if _, ok := stackDecomposedChunks(ctx, repo, runID); !ok {
		return stackPlanRunNotApplicable
	}
	if failed, _ := stackPlanRunFailureReason(ctx, root, store, repo, runID); failed {
		return stackPlanRunFailed
	}
	policy := cliworkflow.StackPlanMergePolicy(ctx, repo, runID)
	if !cliworkflow.StackDriveCompleted(ctx, root, store, repo, runID, policy, remoteMergeOracle) {
		return stackPlanRunIncomplete
	}
	return stackPlanRunComplete
}

// classifyStackPlanRunDeliveryFn is a seam over classifyStackPlanRunDelivery:
// a test can substitute a fake returning a value outside the 4 declared
// stackPlanRunGate constants to exercise a caller's fail-closed default case,
// which the real function can never produce (its only 4 return statements
// name the 4 constants).
var classifyStackPlanRunDeliveryFn = classifyStackPlanRunDelivery

// stackPlanRunFailureReason reports whether a multi-chunk stack plan run has
// already reached a terminal failure state. A durably failed chunk task, or
// an integration run that settled to a status with NO outgoing transition
// (failed/canceled/timed_out - see workflowledger.ValidRunTransition), means
// the stack cannot complete and the plan run should be fail-settled rather
// than refused as merely incomplete.
//
// delivery_failed is deliberately NOT in that set. It is the REPAIRABLE
// delivery state: ValidRunTransition gives delivery_failed ->
// delivery_pending|delivery_failed|running, cliworkflow.DeliverRunWithStore re-delivers
// such a run, and waitIntegrationRunSettled tells the operator to "fix the
// refusal and resume or re-deliver before the stack can complete". Counting
// it as terminal made a commit hook that rejected the integration PR
// fail-settle the PLAN run to failed - a status with no outgoing edges - so
// the operator could repair the integration run but never revive the plan
// run. A delivery_failed integration run classifies as INCOMPLETE.
// stackPlanRunFailureReasonFn is a seam over stackPlanRunFailureReason: a
// test can substitute a fake returning failed=true with an empty reason to
// exercise a caller's defensive empty-reason fallback, a combination the
// real function's invariant (every failed=true return also sets a non-empty
// reason) never produces on its own.
var stackPlanRunFailureReasonFn = stackPlanRunFailureReason

func stackPlanRunFailureReason(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string) (failed bool, reason string) {
	byID, err := stackTaskMap(workflowledger.NewStore(store), runID)
	if err == nil {
		for taskID, task := range byID {
			if task.Status == stackStatusFailed {
				return true, fmt.Sprintf("chunk %s failed terminally", taskID)
			}
			// A canceled chunk is terminal and unimplemented: its dependency
			// died, so the stack can never complete either.
			if task.Status == stackStatusCanceled {
				return true, fmt.Sprintf("chunk %s was canceled after its dependency failed", taskID)
			}
		}
	}
	intRun, found, err := stackRunRef(repo, runID, stackIntegrationChunkID)
	if err == nil && found {
		switch intRun.Status {
		case workflowledger.RunStatusFailed, workflowledger.RunStatusCanceled,
			workflowledger.RunStatusTimedOut:
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
