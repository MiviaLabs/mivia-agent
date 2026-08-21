package localengine

import (
	"context"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// resumeExistingInvocation turns a resent invocation into resume when no
// controller in this engine still owns the same non-terminal run.
func (e *Engine) resumeExistingInvocation(ctx context.Context, run workflowledger.RunSnapshot, req workflowledger.StartRequest) (workflowledger.StartResult, bool, error) {
	if workflowledger.IsTerminalRunStatus(run.Status) || run.Status == workflowledger.RunStatusDeliveryPending {
		return workflowledger.StartResult{}, false, nil
	}
	e.mu.Lock()
	_, active := e.active[run.RunID]
	e.mu.Unlock()
	if active {
		return workflowledger.StartResult{}, false, nil
	}
	result, err := e.resume(ctx, workflowledger.StartRequest{Resume: true, RunID: run.RunID, Force: req.Force, AllowPublish: req.AllowPublish})
	return result, true, err
}
