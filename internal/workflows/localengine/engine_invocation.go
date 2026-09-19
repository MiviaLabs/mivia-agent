package localengine

import (
	"context"

	workflowagenttools "github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// resumeExistingInvocation turns a resent invocation into resume when no
// controller in this engine still owns the same non-terminal run.
func (e *Engine) resumeExistingInvocation(ctx context.Context, run workflowledger.RunSnapshot, req workflowagenttools.StartRequest) (workflowagenttools.StartResult, bool, error) {
	if workflowledger.IsTerminalRunStatus(run.Status) || run.Status == workflowledger.RunStatusDeliveryPending {
		return workflowagenttools.StartResult{}, false, nil
	}
	e.mu.Lock()
	_, active := e.active[run.RunID]
	e.mu.Unlock()
	if active {
		return workflowagenttools.StartResult{}, false, nil
	}
	result, err := e.resume(ctx, workflowagenttools.StartRequest{Resume: true, RunID: run.RunID, Force: req.Force, AllowPublish: req.AllowPublish})
	return result, true, err
}
