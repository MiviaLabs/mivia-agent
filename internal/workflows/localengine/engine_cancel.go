package localengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// panelCancelCoordinator returns the coordinator that can inspect and cancel
// exact panel children for runID through the same coordinator instance this
// engine actually dispatches them with. When runID is live in this process
// (active != nil), it reuses active.ctrl.Runner's coordinator: that is the
// exact instance the in-flight Advance() call dispatched panel children
// with, holding the in-memory handle (and claim holder identity) needed to
// genuinely cancel a live local-actor member instead of merely refusing a
// held claim (D15). Only when runID is not live in this process does it fall
// back to a fresh coordinator from e.NewRunner, matching prior behavior for
// cross-process/recovered cancellation. Returns nil when neither source
// yields a *controller.CoordinatorRunner (e.g. a scripted test runner):
// CancelRunWithAttemptsWithClaim then fails closed only if it actually
// finds a live panel attempt to reconcile.
func (e *Engine) panelCancelCoordinator(active *activeRun) coordinator.Coordinator {
	if active != nil && active.ctrl != nil {
		if runner, ok := active.ctrl.Runner.(*controller.CoordinatorRunner); ok {
			return runner.Coordinator
		}
	}
	if e.NewRunner == nil {
		return nil
	}
	runner, ok := e.NewRunner().(*controller.CoordinatorRunner)
	if !ok {
		return nil
	}
	return runner.Coordinator
}

// Cancel implements workflowledger.Engine.
func (e *Engine) Cancel(ctx context.Context, runID string) (workflowledger.CancelResult, error) {
	if e == nil || e.Repo == nil {
		return workflowledger.CancelResult{}, fmt.Errorf("workflow engine is incomplete")
	}
	e.mu.Lock()
	_, delivering := e.delivering[runID]
	active, ok := e.active[runID]
	e.mu.Unlock()
	if delivering {
		// A delivery is mid-publish in THIS engine. Refuse without touching
		// any claim: clearing the live delivery claim would let a second
		// publisher strip the exclusion fence and double-publish.
		return workflowledger.CancelResult{}, fmt.Errorf("run %q is being delivered; cancel refused", runID)
	}
	if ok {
		active.cancel()
		// Wait for the controller to drop its claim so CancelRun can settle.
		select {
		case <-active.done:
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	// Never clear a held claim: cancelTool accepts any run_id, and a blind
	// clear would strip a live delivery claim (held by this or another host
	// mid-publish) and enable double-publish. Claim instead; an expired lease
	// may be taken over with the exclusion fence still armed, but a fresh
	// foreign claim is refused outright.
	holder := "wfcancel-" + randomToken(5)
	if err := e.claimOrTakeoverExpired(ctx, runID, holder); err != nil {
		return workflowledger.CancelResult{}, err
	}
	defer func() { _ = e.Repo.ReleaseRun(context.Background(), runID, holder) }()
	attempts, err := controller.CancelRunWithAttemptsWithClaim(ctx, e.Repo, e.panelCancelCoordinator(active), runID, holder)
	if err != nil {
		// Context cancel may already have settled the run; treat terminal as success.
		run, getErr := e.Repo.GetRun(ctx, runID)
		if getErr == nil && workflowledger.IsTerminalRunStatus(run.Status) {
			e.forgetWorktree(runID)
			return workflowledger.CancelResult{RunID: runID, Status: string(run.Status)}, nil
		}
		return workflowledger.CancelResult{}, err
	}
	// Terminal progress: one step_completed(canceled) per attempt the cancel
	// settled, so hosts observing the engine see the operator cancel.
	emitCanceledAttempts(runID, attempts)
	run, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		return workflowledger.CancelResult{}, err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		e.forgetWorktree(runID)
	}
	return workflowledger.CancelResult{RunID: runID, Status: string(run.Status)}, nil
}

// claimOrTakeoverExpired claims runID for holder, taking over the existing
// claim only when its lease has actually expired. A fresh foreign claim is
// refused outright: a live delivery claim (held by this or another host
// mid-publish) must never be force-cleared by cancel.
func (e *Engine) claimOrTakeoverExpired(ctx context.Context, runID, holder string) error {
	if err := e.Repo.ClaimRun(ctx, runID, holder); err != nil {
		if errors.Is(err, workflowledger.ErrClaimHeld) {
			takeoverErr := e.Repo.TakeoverExpiredRunClaim(ctx, runID, holder, runClaimLease)
			if errors.Is(takeoverErr, workflowledger.ErrClaimNotHeld) {
				return e.Repo.ClaimRun(ctx, runID, holder)
			}
			if takeoverErr != nil {
				return fmt.Errorf("workflow run %q is claimed by another executor; cancel refused", runID)
			}
			return nil
		}
		return err
	}
	return nil
}
