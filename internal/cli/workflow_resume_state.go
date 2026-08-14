package cli

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// loadWorkflowResumeState reads and validates everything a resume needs from
// the ledger before any hook or controller is built: the run row, its
// digest-checked admission snapshot, and the recompiled definition.
func loadWorkflowResumeState(ctx context.Context, repo workflowledger.Repository, runID string, res *config.Resolved) (workflowledger.RunSnapshot, workflowledger.Snapshot, []byte, *compiler.CompiledWorkflow, map[string]any, error) {
	fail := func(err error) (workflowledger.RunSnapshot, workflowledger.Snapshot, []byte, *compiler.CompiledWorkflow, map[string]any, error) {
		return workflowledger.RunSnapshot{}, workflowledger.Snapshot{}, nil, nil, nil, err
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return fail(err)
	}
	if err := refuseWorkflowDeliverySettled(runID, run.Status); err != nil {
		return fail(err)
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return fail(err)
	}
	snapshot, compiled, inputs, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		return fail(err)
	}
	if err := validateWorkflowMCPConfigDigest(snapshot, res.MCP); err != nil {
		return fail(err)
	}
	return run, snapshot, raw, compiled, inputs, nil
}

func validateWorkflowMCPConfigDigest(snapshot workflowledger.Snapshot, current config.MCPConfig) error {
	if snapshot.MCPConfigDigest == "" {
		if current.Enabled && len(current.Servers) > 0 {
			return fmt.Errorf("workflow snapshot does not pin the enabled MCP configuration")
		}
		return nil
	}
	digest, err := config.MCPConfigDigest(current)
	if err != nil {
		return err
	}
	if digest != snapshot.MCPConfigDigest {
		return fmt.Errorf("MCP configuration changed since workflow admission")
	}
	return nil
}
