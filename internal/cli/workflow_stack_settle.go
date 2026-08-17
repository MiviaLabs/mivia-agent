package cli

import (
	"context"
	"errors"
	"log"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// settleStackPlanRunFailed CAS-settles a delivery_pending stacking plan run to
// failed when its stack is terminally uncompletable. It is a no-op when the
// run is absent, already terminal, or not parked at delivery_pending. The
// caller must hold the run's execution flock/claim; this helper does not
// claim.
func settleStackPlanRunFailed(ctx context.Context, repo workflowledger.Repository, runID string, cause string) error {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return nil
		}
		return err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		return nil
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		return nil
	}
	log.Printf("workflow: plan run %s failed: %s", runID, cause)
	return repo.CompareAndSetRunStatus(ctx, runID, run.Version, workflowledger.RunStatusFailed, nil)
}

// refuseFailedStackPlanRunDelivery is the `workflow deliver` branch for a plan
// run whose stack terminally failed. It NEVER publishes: a stack that lost a
// chunk (or whose integration run reached a status with no outgoing
// transition) never delivered the plan the PR would describe, so publishing it
// and settling the run succeeded would report work that does not exist.
//
// It settles as well as refuses, for two reasons. First, the caller holds the
// run's execution flock (beginWorkflowExecutionBounded), which is the same
// precondition the drive and sweep paths settle under, so the CAS cannot race
// a concurrent driver. Second, refusal alone is a dead end: the failure
// statuses have no repair edge, so the plan run would sit at delivery_pending
// with `deliver` refusing it on every retry and no other command able to close
// it. settleStackPlanRunFailed is a no-op for a run that is absent, already
// terminal, or not parked at delivery_pending, so a repeated deliver stays
// idempotent and still returns the refusal for a non-zero exit.
func refuseFailedStackPlanRunDelivery(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string) error {
	_, reason := stackPlanRunFailureReason(ctx, root, store, repo, runID)
	if reason == "" {
		reason = "stack terminally failed"
	}
	if err := settleStackPlanRunFailed(ctx, repo, runID, reason); err != nil {
		return err
	}
	return errFailedStackPlanRun(runID, reason)
}

// settleFailedStackPlanRunIfNeeded fail-settles a delivery_pending stacking
// plan run whose stack terminally failed (see stackPlanRunFailureReason),
// reporting whether it settled. Used by the in-session drive paths so a dead
// stack settles once instead of being refused as merely incomplete forever.
func settleFailedStackPlanRunIfNeeded(ctx context.Context, prepared *preparedWorkflowRun, runID, cause string) (bool, error) {
	if classifyStackPlanRunDelivery(ctx, prepared.root, prepared.store, prepared.repo, runID, true) != stackPlanRunFailed {
		return false, nil
	}
	if err := settleStackPlanRunFailed(ctx, prepared.repo, runID, cause); err != nil {
		return false, err
	}
	return true, nil
}

// settleFailedStackPlanRunIfNeededFn is a seam over
// settleFailedStackPlanRunIfNeeded: executeWorkflowRun builds its own store
// and repo internally with no injectable seam of its own, so a test that
// needs to force this function's settleErr branch (a transient store fault
// during the CAS itself) substitutes a fake here instead.
var settleFailedStackPlanRunIfNeededFn = settleFailedStackPlanRunIfNeeded
