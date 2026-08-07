package controller

import (
	"context"
	"fmt"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// CancelRun cancels a run that is not yet terminal: it CASes the run status
// to canceled (a pending run moves through running first, since
// pending->canceled is not a valid edge) and marks every non-terminal attempt
// canceled. It mints its own claim holder and refuses delivery_pending runs
// (those must be delivered or cleaned up before cancel). Cancel is idempotent
// and re-runnable: a crash between the two pending CASes leaves a resumable
// running run that a re-run of CancelRun settles.
//
// The run claim protects concurrent executors; the caller is expected to hold
// the workflow execution file lock and clear a stale claim before calling.
func CancelRun(ctx context.Context, repo workflowledger.Repository, runID string) error {
	if repo == nil {
		return fmt.Errorf("workflow ledger is nil")
	}
	holder := newWorkflowHolder()
	ctx = workflowledger.ContextWithClaimHolder(ctx, holder)
	if err := repo.ClaimRun(ctx, runID, holder); err != nil {
		return err
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }()

	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		// Already finished (idempotent operator retry).
		return nil
	}
	if run.Status == workflowledger.RunStatusDeliveryPending {
		return fmt.Errorf("run %q is waiting for delivery; deliver it or leave it for cleanup before cancel", runID)
	}
	if run.Status == workflowledger.RunStatusPending {
		if err := repo.CompareAndSetRunStatus(ctx, runID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
			return err
		}
		run, err = repo.GetRun(ctx, runID)
		if err != nil {
			return err
		}
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, run.Version, workflowledger.RunStatusCanceled, nil); err != nil {
		return err
	}
	// Mark every non-terminal attempt canceled. Attempt marking is
	// best-effort display data: the run status is authoritative, and an
	// attempt that completed meanwhile is simply skipped (the claim fences
	// concurrent completion anyway).
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		if workflowledger.IsTerminalAttemptStatus(attempt.Status) {
			continue
		}
		_ = repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, attempt.Version, workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusCanceled})
	}
	return nil
}
