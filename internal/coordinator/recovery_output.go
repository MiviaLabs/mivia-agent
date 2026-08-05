package coordinator

import (
	"context"
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func (c *coordinator) terminalTaskResult(snap ledger.TaskSnapshot) (subagents.Result, bool) {
	result, terminal := terminalTaskResult(snap)
	if !terminal || snap.OutputRef == "" {
		if terminal {
			result.Provenance = runtime.Metadata{Kind: "recovered", Status: snap.Status}
		}
		return result, terminal
	}
	result.Provenance = runtime.Metadata{Kind: "recovered", Status: snap.Status}
	output, err := c.repo.LoadContent(context.Background(), snap.OutputRef)
	if err != nil {
		return result, true
	}
	result.Output = append(json.RawMessage(nil), output...)
	return result, true
}

func (c *coordinator) resultsFromSnapshots(tasks []ledger.TaskSnapshot) []subagents.Result {
	results := make([]subagents.Result, len(tasks))
	for i, task := range tasks {
		result, terminal := c.terminalTaskResult(task)
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
