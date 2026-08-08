package localengine

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Delete implements agenttools.Engine. It removes a settled run (terminal or
// delivery_pending) from the durable ledger. It mirrors Cancel's fencing: an
// in-process delivery or controller on this engine refuses, a fresh foreign
// claim refuses, and only an expired claim may be taken over — deletion must
// never blind-clear a live delivery claim. The status gate runs BEFORE any
// claim mutation, so a refused delete leaves claims untouched.
func (e *Engine) Delete(ctx context.Context, runID string) (agenttools.DeleteResult, error) {
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
	if !workflowledger.IsDeletableRunStatus(run.Status) {
		return agenttools.DeleteResult{}, fmt.Errorf("run %q is %q; cancel it before delete", runID, run.Status)
	}
	holder := "wfdelete-" + randomToken(5)
	if err := e.claimOrTakeoverExpired(ctx, runID, holder); err != nil {
		return agenttools.DeleteResult{}, err
	}
	// DeleteRun appends the wf_run_deleted tombstone claim-fenced to holder,
	// then the store removes the run's prior events AND the claim row, so no
	// release is needed (or possible) after success.
	ctx = workflowledger.ContextWithClaimHolder(ctx, holder)
	if err := e.Repo.DeleteRun(ctx, runID); err != nil {
		return agenttools.DeleteResult{}, err
	}
	return agenttools.DeleteResult{RunID: runID, Status: string(run.Status), Deleted: true}, nil
}
