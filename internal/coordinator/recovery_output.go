package coordinator

import (
	"context"
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// terminalTaskResultWithOutput wraps the package-level terminalTaskResult
// with optional output content loading. Unlike the package-level function,
// it loads the output ref when available and sets result.Output.
// Accepts a context so LoadContent respects cancellation/deadlines.
func (c *coordinator) terminalTaskResultWithOutput(ctx context.Context, snap ledger.TaskSnapshot) (subagents.Result, bool) {
	result, terminal := terminalTaskResult(snap)
	if !terminal || snap.OutputRef == "" {
		if terminal {
			result.Provenance = runtime.Metadata{Kind: "recovered", Status: snap.Status}
		}
		return result, terminal
	}
	result.Provenance = runtime.Metadata{Kind: "recovered", Status: snap.Status}
	output, err := c.repo.LoadContent(ctx, snap.OutputRef)
	if err != nil {
		return result, true
	}
	result.Output = append(json.RawMessage(nil), output...)
	return result, true
}

func (c *coordinator) resultsFromSnapshots(ctx context.Context, tasks []ledger.TaskSnapshot) []subagents.Result {
	results := make([]subagents.Result, len(tasks))
	for i, task := range tasks {
		result, terminal := c.terminalTaskResultWithOutput(ctx, task)
		if terminal {
			results[i] = result
			continue
		}
		results[i] = subagents.Result{TaskID: task.TaskID, Status: task.Status, Provenance: runtime.Metadata{Kind: "recovered", Status: task.Status}}
		if isRecoveredTaskFailure(task.Status) {
			results[i].Err = recoveredTaskError(task.TaskID, task.Status, task.ErrorRef)
		}
	}
	return results
}
