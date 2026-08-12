package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// CancelRun cancels a run that is not yet terminal. It is a thin wrapper
// around CancelRunWithAttempts for callers that only drive the status
// transition and do not need the attempts that were canceled.
func CancelRun(ctx context.Context, repo workflowledger.Repository, coord coordinator.Coordinator, runID string) error {
	_, err := CancelRunWithAttempts(ctx, repo, coord, runID)
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
// coord is the panel control dependency (D15): it cancels or tombstones a
// live panel step's exact member and synthesis children before the run is
// allowed to report canceled. A nil coord is only safe when no attempt can
// ever carry a PanelExecution (panelsEnabled is false as of Wave 6); a
// caller that reaches a real panel attempt without a coord fails closed with
// a clear error rather than silently orphaning the panel's children.
//
// The run claim protects concurrent executors; the caller is expected to hold
// the workflow execution file lock and clear a stale claim before calling.
// Callers emit one step_completed event per returned attempt: each carries
// the canceled status and the operator-cancel ErrorRef.
func CancelRunWithAttempts(ctx context.Context, repo workflowledger.Repository, coord coordinator.Coordinator, runID string) ([]workflowledger.StepAttempt, error) {
	if repo == nil {
		return nil, fmt.Errorf("workflow ledger is nil")
	}
	holder := newWorkflowHolder()
	if err := repo.ClaimRun(ctx, runID, holder); err != nil {
		return nil, err
	}
	defer func() { _ = repo.ReleaseRun(context.Background(), runID, holder) }()
	return CancelRunWithAttemptsWithClaim(ctx, repo, coord, runID, holder)
}

// CancelRunWithAttemptsWithClaim settles a run with holder's existing claim.
// The caller must hold the claim and release it after this function returns.
//
// A run whose active attempt carries a live PanelExecution is reconciled
// through ReconcilePanelCancellation first (D15): the run is never CASed to
// canceled until every intended member and synthesis child is confirmed
// terminal. If reconciliation reports ErrCancelBlocked (an ambiguous child
// claim) or ErrCancelPending (a slow child that has not settled yet), this
// function returns that error and leaves the run non-terminal and the
// workflow claim untouched, so a later cancel or resume can retry the same
// idempotent reconciliation. Non-panel attempts are unaffected: they keep
// the existing best-effort "mark canceled" behavior.
func CancelRunWithAttemptsWithClaim(ctx context.Context, repo workflowledger.Repository, coord coordinator.Coordinator, runID, holder string) ([]workflowledger.StepAttempt, error) {
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

	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return nil, err
	}
	var panelAttempt *workflowledger.StepAttempt
	for i := range attempts {
		if attempts[i].PanelExecution != nil && !workflowledger.IsTerminalAttemptStatus(attempts[i].Status) {
			panelAttempt = &attempts[i]
			break
		}
	}
	var canceled []workflowledger.StepAttempt
	if panelAttempt != nil {
		reconciled, err := cancelPanelAttempt(ctx, repo, coord, runID, holder, *panelAttempt)
		if err != nil {
			return nil, err
		}
		canceled = append(canceled, reconciled)
	}

	// D13: refresh the claim immediately before the terminal write. Panel
	// reconciliation above can take real wall-clock time (a coordinator.Cancel
	// call per member/synthesis child); run.Version only changes on RunStatus
	// transitions, so without this refresh a caller whose claim lease expired
	// and was legitimately taken over mid-reconciliation could still win the
	// version-based CAS below and finalize the run out from under the new
	// holder. A lost claim surfaces here as an error to the caller, same as
	// any other durable failure in this function.
	if err := repo.ClaimRun(ctx, runID, holder); err != nil {
		return nil, err
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, run.Version, workflowledger.RunStatusCanceled, nil); err != nil {
		return nil, err
	}
	remaining, err := markRemainingAttemptsCanceled(ctx, repo, runID)
	if err != nil {
		return nil, err
	}
	return append(canceled, remaining...), nil
}

// markRemainingAttemptsCanceled marks every non-terminal attempt canceled
// after the run itself is already settled. Attempt marking is best-effort
// display data: the run status is authoritative, and an attempt that
// completed meanwhile is simply skipped (the claim fences concurrent
// completion anyway). The canceled attempts are returned so callers can
// emit one step_completed event per attempt.
func markRemainingAttemptsCanceled(ctx context.Context, repo workflowledger.Repository, runID string) ([]workflowledger.StepAttempt, error) {
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

// cancelPanelAttempt reconciles one panel attempt to a terminal canceled
// state before the run itself is allowed to report canceled (D15). It
// returns the attempt with its terminal status recorded, or propagates
// ErrCancelBlocked/ErrCancelPending/a durable error without settling
// anything, leaving the run and the attempt's phase exactly as
// ReconcilePanelCancellation last observed them for a later retry.
func cancelPanelAttempt(ctx context.Context, repo workflowledger.Repository, coord coordinator.Coordinator, runID, holder string, attempt workflowledger.StepAttempt) (workflowledger.StepAttempt, error) {
	if coord == nil {
		return workflowledger.StepAttempt{}, fmt.Errorf("panel attempt %q requires a coordinator to cancel; none was supplied", attempt.AttemptID)
	}
	panel := workflowledger.NewPanelCoordinator(runID, coord, repo)
	reconciled, allTerminal, err := ReconcilePanelCancellation(ctx, repo, panel, runID, holder, attempt.AttemptID)
	if err != nil {
		return workflowledger.StepAttempt{}, err
	}
	if !allTerminal {
		return workflowledger.StepAttempt{}, ErrCancelPending
	}
	if workflowledger.IsTerminalAttemptStatus(reconciled.Status) {
		return reconciled, nil
	}
	outcome := workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusCanceled}
	outcome.ErrorRef = storeErrorText(ctx, repo, errors.New("workflow run canceled by operator"))
	if err := repo.CompleteStepAttempt(ctx, runID, reconciled.AttemptID, reconciled.Version, outcome); err != nil {
		return workflowledger.StepAttempt{}, err
	}
	reconciled.Status = workflowledger.AttemptStatusCanceled
	reconciled.ErrorRef = outcome.ErrorRef
	return reconciled, nil
}
