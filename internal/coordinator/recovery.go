package coordinator

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

var (
	recoveredHandles            sync.Map
	errRecoveredRunNotResumable = errors.New("recovered run is nonterminal and has no live execution owner")
)

func (c *Coordinator) recoverByIdempotencyKey(ctx context.Context, key string) (*RunHandle, bool, error) {
	snap, err := c.repo.GetRunByIdempotencyKey(ctx, key)
	if errors.Is(err, ledger.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("recover idempotent run: %w", err)
	}
	tasks, err := c.repo.ListTasks(ctx, snap.RunID)
	if err != nil {
		return nil, false, fmt.Errorf("recover idempotent tasks: %w", err)
	}
	attempts := make(map[string]string, len(tasks))
	for _, task := range tasks {
		if len(task.Attempts) == 0 {
			continue
		}
		latest := task.Attempts[0]
		for _, attempt := range task.Attempts[1:] {
			if attempt.AttemptNum > latest.AttemptNum {
				latest = attempt
			}
		}
		attempts[task.TaskID] = latest.AttemptID
	}
	h := c.newRunHandle(snap.RunID, key, attempts)
	recoveredHandles.Store(h, struct{}{})
	go c.watchRecoveredRun(h)
	return h, true, nil
}

func (c *Coordinator) watchRecoveredRun(h *RunHandle) {
	snap, err := c.repo.GetRun(context.Background(), h.runID)
	result := &RunResult{Snapshot: snap, Err: err}
	if err == nil {
		result.Results = resultsFromSnapshots(snap.Tasks)
		if !isTerminalRunStatus(snap.Status) {
			result.Err = errRecoveredRunNotResumable
		}
	}
	h.mu.Lock()
	h.result = result
	h.mu.Unlock()
	close(h.done)
}

func resultsFromSnapshots(tasks []ledger.TaskSnapshot) []subagents.Result {
	results := make([]subagents.Result, len(tasks))
	for i, task := range tasks {
		results[i] = subagents.Result{
			TaskID: task.TaskID, Status: task.Status,
			Provenance: runtime.Metadata{Kind: "recovered", Status: task.Status},
		}
		if task.ErrorRef != "" {
			results[i].Err = errors.New(task.ErrorRef)
		}
	}
	return results
}

func isTerminalRunStatus(status ledger.RunStatus) bool {
	switch status {
	case ledger.RunStatusCompleted, ledger.RunStatusFailed, ledger.RunStatusCanceled:
		return true
	default:
		return false
	}
}
