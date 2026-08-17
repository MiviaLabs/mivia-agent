package cli

import (
	"context"
	"errors"
	"log"

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
