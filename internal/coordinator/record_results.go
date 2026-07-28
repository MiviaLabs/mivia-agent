package coordinator

import (
	"crypto/sha256"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func (c *Coordinator) recordRunResults(h *RunHandle, tasks []subagents.Task, results []subagents.Result, runErr error) error {
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
		taskSnap, err := c.repo.GetTask(h.poolCtx, h.runID, t.ID)
		if err != nil {
			runErr = joinError(runErr, fmt.Errorf("read task %q: %w", t.ID, err))
			continue
		}

		newStatus := mapStatus(r)
		casOK := false

		if newStatus == string(ledger.TaskStatusBlocked) {
			casOK = taskSnap.Status == newStatus
			if !casOK {
				if err := c.repo.CompareAndSetTaskStatus(h.poolCtx, h.runID, t.ID, taskSnap.Version, newStatus); err != nil {
					runErr = joinError(runErr, fmt.Errorf("update task %q: %w", t.ID, err))
				} else {
					casOK = true
				}
			}
		} else {
			casErr := c.repo.CompareAndSetTaskStatus(h.poolCtx, h.runID, t.ID, taskSnap.Version, newStatus)
			if casErr == nil {
				casOK = true
				// Store output refs (bounded/redacted references, not raw content).
				outputRef := ""
				errorRef := ""
				if len(r.Output) > 0 {
					outputRef = fmt.Sprintf("ref:output:%d", len(r.Output))
				}
				if r.Err != nil {
					digest := sha256.Sum256([]byte(r.Err.Error()))
					errorRef = fmt.Sprintf("ref:error:%x", digest[:])
				}
				if err := c.repo.SetTaskOutput(h.poolCtx, h.runID, t.ID, outputRef, errorRef); err != nil {
					runErr = joinError(runErr, fmt.Errorf("store task %q output: %w", t.ID, err))
				}
			} else {
				runErr = joinError(runErr, fmt.Errorf("update task %q: %w", t.ID, casErr))
			}
		}

		if casOK {
			finished := c.now()
			if err := c.repo.SetTaskAttempt(h.poolCtx, h.runID, t.ID, h.attempts[t.ID], newStatus, &finished); err != nil {
				runErr = joinError(runErr, fmt.Errorf("update attempt %q: %w", t.ID, err))
			}
			if err := c.repo.AppendEvent(h.poolCtx, ledger.LifecycleEvent{
				ID: newEventID(), RunID: h.runID, Kind: "task_" + newStatus,
				TaskID: t.ID, AttemptID: h.attempts[t.ID],
			}); err != nil {
				runErr = joinError(runErr, fmt.Errorf("append task %q event: %w", t.ID, err))
			}
		}
	}

	return runErr
}
