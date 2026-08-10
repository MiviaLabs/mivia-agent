package controller

import (
	"context"
	"errors"
	"fmt"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// CancelRun cancels a run that is not yet terminal. It is a thin wrapper
// around CancelRunWithAttempts for callers that only drive the status
// transition and do not need the attempts that were canceled.
func CancelRun(ctx context.Context, repo workflowledger.Repository, runID string) error {
	_, err := CancelRunWithAttempts(ctx, repo, runID)
	return err
}

// CancelRunWithAttempts cancels a run that is not yet terminal: it CASes the
// run status to canceled (a pending run moves through running first, since
// pending->canceled is not a valid edge) and marks every non-terminal attempt
// canceled, returning the attempts it canceled. It mints its own claim holder
// and refuses delivery_pending runs (those must be delivered or cleaned up
// before cancel). Cancel is idempotent and re-runnable: a crash between the
// two pending CASes leaves a resumable running run that a re-run settles.
//
// The run claim protects concurrent executors; the caller is expected to hold
// the workflow execution file lock and clear a stale claim before calling.
// Callers emit one step_completed event per returned attempt: each carries
// the canceled status and the operator-cancel ErrorRef.
func CancelRunWithAttempts(ctx context.Context, repo workflowledger.Repository, runID string) ([]workflowledger.StepAttempt, error) {
	if repo == nil {
		return nil, fmt.Errorf("workflow ledger is nil")
	}
	holder := newWorkflowHolder()
	if err := repo.ClaimRun(ctx, runID, holder); err != nil {
		return nil, err
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }()
	return CancelRunWithAttemptsWithClaim(ctx, repo, runID, holder)
}

// CancelRunWithAttemptsWithClaim settles a run with holder's existing claim.
// The caller must hold the claim and release it after this function returns.
func CancelRunWithAttemptsWithClaim(ctx context.Context, repo workflowledger.Repository, runID, holder string) ([]workflowledger.StepAttempt, error) {
	if repo == nil {
		return nil, fmt.Errorf("workflow ledger is nil")
	}
	if holder == "" {
		return nil, fmt.Errorf("workflow claim holder is required")
	}
	ctx = workflowledger.ContextWithClaimHolder(ctx, holder)

	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		// Already finished (idempotent operator retry).
		return nil, nil
	}
	if run.Status == workflowledger.RunStatusDeliveryPending {
		return nil, fmt.Errorf("run %q is waiting for delivery; deliver it or leave it for cleanup before cancel", runID)
	}
	if run.Status == workflowledger.RunStatusPending {
		if err := repo.CompareAndSetRunStatus(ctx, runID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
			return nil, err
		}
		run, err = repo.GetRun(ctx, runID)
		if err != nil {
			return nil, err
		}
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, run.Version, workflowledger.RunStatusCanceled, nil); err != nil {
		return nil, err
	}
	// Mark every non-terminal attempt canceled. Attempt marking is
	// best-effort display data: the run status is authoritative, and an
	// attempt that completed meanwhile is simply skipped (the claim fences
	// concurrent completion anyway). The canceled attempts are returned so
	// callers can emit one step_completed event per attempt.
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return nil, err
	}
	var canceled []workflowledger.StepAttempt
	for _, attempt := range attempts {
		if workflowledger.IsTerminalAttemptStatus(attempt.Status) {
			continue
		}
		outcome := workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusCanceled}
		// Record the operator-cancel cause on the attempt so the CLI can
		// explain why the attempt stopped.
		outcome.ErrorRef = storeErrorText(ctx, repo, errors.New("workflow run canceled by operator"))
		if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, attempt.Version, outcome); err != nil {
			continue
		}
		attempt.Status = workflowledger.AttemptStatusCanceled
		attempt.ErrorRef = outcome.ErrorRef
		canceled = append(canceled, attempt)
	}
	return canceled, nil
}
