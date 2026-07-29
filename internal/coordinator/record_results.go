package coordinator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func (c *coordinator) recordRunResults(h *RunHandle, tasks []subagents.Task, results []subagents.Result, runErr error) error {
	persistCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Record results in ledger.
	resultMap := make(map[string]subagents.Result, len(results))
	for _, r := range results {
		resultMap[r.TaskID] = r
	}

	for _, t := range tasks {
		r, ok := resultMap[t.ID]
		if !ok {
			runErr = joinError(runErr, fmt.Errorf("missing result for task %q", t.ID))
			continue
		}

		// Get current task to find version (post-pool).
		taskSnap, err := c.repo.GetTask(persistCtx, h.runID, t.ID)
		if err != nil {
			runErr = joinError(runErr, fmt.Errorf("read task %q: %w", t.ID, err))
			continue
		}

		newStatus := mapStatus(r)
		casOK := c.tryTaskStatusCAS(persistCtx, h.runID, t.ID, taskSnap, newStatus, &runErr)

		if casOK {
			outputRef, errorRef := resultReferences(r)
			c.persistResultContent(persistCtx, outputRef, errorRef, r)
			if err := c.repo.SetTaskOutput(persistCtx, h.runID, t.ID, outputRef, errorRef); err != nil {
				runErr = joinError(runErr, fmt.Errorf("store task %q output: %w", t.ID, err))
			}

			finished := c.nowLocked()
			if err := c.repo.SetTaskAttempt(persistCtx, h.runID, t.ID, h.attempts[t.ID], newStatus, &finished); err != nil {
				runErr = joinError(runErr, fmt.Errorf("update attempt %q: %w", t.ID, err))
			}
			evt := ledger.LifecycleEvent{
				ID: newEventID(), RunID: h.runID, Kind: "task_" + newStatus,
				TaskID: t.ID, AttemptID: h.attempts[t.ID],
			}
			if err := c.repo.AppendEvent(persistCtx, evt); err != nil {
				runErr = joinError(runErr, fmt.Errorf("append task %q event: %w", t.ID, err))
			} else {
				c.emitLifecycleEvent(evt)
			}
		}
	}

	return runErr
}

// persistResultContent stores the actual output and error bytes in the
// content-addressable store so the references are resolvable.
func (c *coordinator) persistResultContent(ctx context.Context, outputRef, errorRef string, r subagents.Result) {
	if outputRef != "" && len(r.Output) > 0 {
		_ = c.repo.StoreContent(ctx, outputRef, r.Output)
	}
	if errorRef != "" && r.Err != nil {
		_ = c.repo.StoreContent(ctx, errorRef, []byte(r.Err.Error()))
	}
}

// tryTaskStatusCAS attempts a compare-and-set on a task's status, handling
// the special case of blocked status.
func (c *coordinator) tryTaskStatusCAS(ctx context.Context, runID, taskID string, snap ledger.TaskSnapshot, newStatus string, runErr *error) bool {
	if newStatus == string(ledger.TaskStatusBlocked) {
		if snap.Status == newStatus {
			return true
		}
		if err := c.repo.CompareAndSetTaskStatus(ctx, runID, taskID, snap.Version, newStatus); err != nil {
			*runErr = joinError(*runErr, fmt.Errorf("update task %q: %w", taskID, err))
			return false
		}
		return true
	}
	// Skip CAS if already in target state (avoids ErrInvalidTransition
	// when runDAG already terminal-transitioned the task via retry).
	if snap.Status == newStatus {
		return true
	}
	if err := c.repo.CompareAndSetTaskStatus(ctx, runID, taskID, snap.Version, newStatus); err != nil {
		*runErr = joinError(*runErr, fmt.Errorf("update task %q: %w", taskID, err))
		return false
	}
	return true
}

// resultReferences stores bounded references rather than raw task output.
func resultReferences(r subagents.Result) (outputRef, errorRef string) {
	if len(r.Output) > 0 {
		digest := sha256.Sum256(r.Output)
		outputRef = fmt.Sprintf("ref:output:%x", digest[:8])
	}
	if r.Err != nil {
		digest := sha256.Sum256([]byte(r.Err.Error()))
		errorRef = fmt.Sprintf("ref:error:%x", digest[:])
	}
	return outputRef, errorRef
}
