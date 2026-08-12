package controller

import (
	"context"
	"errors"
	"fmt"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ErrCancelBlocked reports that a panel child's terminal state cannot be
// safely verified right now (D15 item 6): an ambiguous recovered claim, or a
// task whose persisted status looks nonterminal with no verifiable live
// owner. Cancellation must fail closed here instead of reporting a false
// "canceled" outcome, and the workflow claim stays held so a later retry can
// make progress.
var ErrCancelBlocked = errors.New("cancel_blocked")

// ErrCancelPending reports that cancel_pending is durably recorded but at
// least one intended child has not reached a terminal state yet (a slow
// worker, D15 item 5). The workflow stays non-terminal; a later resume or
// cancel retry repeats the idempotent reconciliation.
var ErrCancelPending = errors.New("cancel_pending")

// PanelCancelCoordinator performs the durable per-child cancel/tombstone
// operations cancel_pending reconciliation needs. workflowledger.PanelCoordinator
// implements it.
type PanelCancelCoordinator interface {
	CancelOrTombstoneMember(ctx context.Context, attemptID, memberID string) (bool, error)
	CancelOrTombstoneSynthesis(ctx context.Context, attemptID string) (bool, error)
}

const maxCancelPhaseRetries = 4

// ReconcilePanelCancellation drives one panel attempt toward and through
// cancel_pending (D15): it advances the durable phase (refreshing the claim
// immediately before the write, D13), then cancels or tombstones every
// intended child. The caller must already hold the workflow claim (ctx
// carries the holder via workflowledger.ContextWithClaimHolder) and keep
// holding it until this returns.
//
// It returns the attempt as last observed, allTerminal=true once every
// intended child (and the attempt phase itself, if already terminal) is
// terminal, or a non-nil error: ErrCancelBlocked when a child's terminal
// state is ambiguous, ErrCancelPending when children are known but not yet
// all terminal, or a wrapped durable error otherwise.
func ReconcilePanelCancellation(ctx context.Context, repo workflowledger.Repository, panel PanelCancelCoordinator, runID, holder, attemptID string) (workflowledger.StepAttempt, bool, error) {
	attempt, err := repo.GetStepAttempt(ctx, runID, attemptID)
	if err != nil {
		return workflowledger.StepAttempt{}, false, err
	}
	if workflowledger.IsTerminalAttemptStatus(attempt.Status) {
		return attempt, true, nil
	}
	if attempt.PanelExecution == nil {
		return attempt, false, fmt.Errorf("attempt %q has no panel execution", attemptID)
	}
	attempt, err = advancePanelPhaseToCancelPending(ctx, repo, runID, holder, attempt)
	if err != nil {
		return attempt, false, err
	}
	if workflowledger.IsTerminalAttemptStatus(attempt.Status) {
		return attempt, true, nil
	}

	allTerminal := true
	for _, member := range attempt.PanelExecution.Members {
		terminal, cancelErr := panel.CancelOrTombstoneMember(ctx, attemptID, member.MemberID)
		if cancelErr != nil {
			return attempt, false, fmt.Errorf("%w: member %q: %v", ErrCancelBlocked, member.MemberID, cancelErr)
		}
		allTerminal = allTerminal && terminal
	}
	if attempt.PanelExecution.Synthesis != nil {
		terminal, cancelErr := panel.CancelOrTombstoneSynthesis(ctx, attemptID)
		if cancelErr != nil {
			return attempt, false, fmt.Errorf("%w: synthesis: %v", ErrCancelBlocked, cancelErr)
		}
		allTerminal = allTerminal && terminal
	}
	return attempt, allTerminal, nil
}

// advancePanelPhaseToCancelPending CASes the attempt's panel phase into
// cancel_pending from its current legal phase, refreshing the claim
// immediately before the write (D13). A lost CAS reloads durable state and
// retries from whatever phase actually won the race (another holder's
// concurrent members_admitted -> synthesis_admitted transition, or a
// concurrent cancel_pending transition that already won). Already-terminal
// or already-cancel_pending attempts are returned unchanged.
func advancePanelPhaseToCancelPending(ctx context.Context, repo workflowledger.Repository, runID, holder string, attempt workflowledger.StepAttempt) (workflowledger.StepAttempt, error) {
	if holder == "" {
		return attempt, workflowledger.ErrClaimNotHeld
	}
	for i := 0; i < maxCancelPhaseRetries; i++ {
		if workflowledger.IsTerminalAttemptStatus(attempt.Status) || attempt.PanelExecution.Phase == workflowledger.PanelPhaseCancelPending {
			return attempt, nil
		}
		from := attempt.PanelExecution.Phase
		if from != workflowledger.PanelPhaseMembersAdmitted && from != workflowledger.PanelPhaseSynthesisAdmitted {
			return attempt, fmt.Errorf("panel attempt %q has no legal cancel transition from phase %q", attempt.AttemptID, from)
		}
		if err := repo.ClaimRun(ctx, runID, holder); err != nil {
			return attempt, err
		}
		err := repo.CompareAndSetPanelPhase(ctx, runID, attempt.AttemptID, attempt.Version, from, workflowledger.PanelPhaseCancelPending, nil)
		if err == nil {
			return repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
		}
		if !errors.Is(err, workflowledger.ErrConflict) {
			return attempt, err
		}
		reloaded, getErr := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
		if getErr != nil {
			return attempt, getErr
		}
		attempt = reloaded
	}
	return attempt, fmt.Errorf("panel attempt %q: cancel phase transition did not converge", attempt.AttemptID)
}
