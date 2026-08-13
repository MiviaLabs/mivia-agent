package localengine

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Delete implements agenttools.Engine. It removes a run from the durable
// ledger. Settled runs (terminal or delivery_pending) are always deletable;
// force also permits a non-terminal run (pending/running/waiting_approval) —
// the crash-recovery override for a run stranded by a dead executor. It
// mirrors Cancel's fencing: an in-process delivery or controller on this
// engine refuses, a fresh foreign claim refuses, and only an expired claim may
// be taken over — deletion must never blind-clear a live delivery claim. The
// status gate runs BEFORE any claim mutation, so a refused delete leaves
// claims untouched.
func (e *Engine) Delete(ctx context.Context, runID string, force bool) (agenttools.DeleteResult, error) {
	if e == nil || e.Repo == nil {
		return agenttools.DeleteResult{}, fmt.Errorf("workflow engine is incomplete")
	}
	e.mu.Lock()
	_, delivering := e.delivering[runID]
	_, active := e.active[runID]
	e.mu.Unlock()
	if delivering {
		return agenttools.DeleteResult{}, fmt.Errorf("run %q is being delivered; delete refused", runID)
	}
	if active {
		return agenttools.DeleteResult{}, fmt.Errorf("run %q is running in this engine; cancel it before delete", runID)
	}
	run, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.DeleteResult{}, err
	}
	if !workflowledger.IsDeletableRunStatus(run.Status) && !force {
		return agenttools.DeleteResult{}, fmt.Errorf("run %q is %q; cancel it before delete, or pass force only after the prior executor stopped (a live claim is still refused)", runID, run.Status)
	}
	holder := "wfdelete-" + randomToken(5)
	if err := e.claimOrTakeoverExpired(ctx, runID, holder); err != nil {
		return agenttools.DeleteResult{}, err
	}
	// DeleteRun atomically appends the wf_run_deleted tombstone and removes the
	// run's prior events and claim row. No release is needed after success.
	ctx = workflowledger.ContextWithClaimHolder(ctx, holder)
	if err := e.Repo.DeleteRun(ctx, runID); err != nil {
		return agenttools.DeleteResult{}, err
	}
	e.forgetWorktree(runID)
	return agenttools.DeleteResult{RunID: runID, Status: string(run.Status), Deleted: true}, nil
}
