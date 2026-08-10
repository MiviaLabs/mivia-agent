package localengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Cancel implements agenttools.Engine.
func (e *Engine) Cancel(ctx context.Context, runID string) (agenttools.CancelResult, error) {
	if e == nil || e.Repo == nil {
		return agenttools.CancelResult{}, fmt.Errorf("workflow engine is incomplete")
	}
	e.mu.Lock()
	_, delivering := e.delivering[runID]
	active, ok := e.active[runID]
	e.mu.Unlock()
	if delivering {
		// A delivery is mid-publish in THIS engine. Refuse without touching
		// any claim: clearing the live delivery claim would let a second
		// publisher strip the exclusion fence and double-publish.
		return agenttools.CancelResult{}, fmt.Errorf("run %q is being delivered; cancel refused", runID)
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
		return agenttools.CancelResult{}, err
	}
	// CancelRunWithAttempts mints and claims its own holder internally; drop
	// ours first so its claim succeeds. Never clear a foreign claim.
	_ = e.Repo.ReleaseRun(context.Background(), runID, holder)
	attempts, err := controller.CancelRunWithAttempts(ctx, e.Repo, runID)
	if err != nil {
		// Context cancel may already have settled the run; treat terminal as success.
		run, getErr := e.Repo.GetRun(ctx, runID)
		if getErr == nil && workflowledger.IsTerminalRunStatus(run.Status) {
			return agenttools.CancelResult{RunID: runID, Status: string(run.Status)}, nil
		}
		return agenttools.CancelResult{}, err
	}
	// Terminal progress: one step_completed(canceled) per attempt the cancel
	// settled, so hosts observing the engine see the operator cancel.
	emitCanceledAttempts(runID, attempts)
	run, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.CancelResult{}, err
	}
	return agenttools.CancelResult{RunID: runID, Status: string(run.Status)}, nil
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
