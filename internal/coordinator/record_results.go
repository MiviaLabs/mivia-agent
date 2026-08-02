package coordinator

import (
	"context"
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
			outputRef, errorRef = c.persistResultContent(persistCtx, outputRef, errorRef, r, &runErr)
			if err := c.repo.SetTaskOutput(persistCtx, h.runID, t.ID, outputRef, errorRef); err != nil {
				runErr = joinError(runErr, fmt.Errorf("store task %q output: %w", t.ID, err))
			}

			finished := c.nowLocked()
			if err := c.repo.SetTaskAttempt(persistCtx, h.runID, t.ID, h.getAttempt(t.ID), newStatus, &finished); err != nil {
				runErr = joinError(runErr, fmt.Errorf("update attempt %q: %w", t.ID, err))
			}
			evt := ledger.LifecycleEvent{
				ID: newEventID(), RunID: h.runID, Kind: "task_" + newStatus,
				TaskID: t.ID, AttemptID: h.getAttempt(t.ID),
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

// persistResultContent stores the output and error bytes in the
// content-addressable store and returns the references that are actually
// resolvable. A reference whose content write failed is dropped rather than
// recorded, so a ref on a task always resolves. The write error is joined into
// runErr on the same terms as the sibling persistence failures in
// recordRunResults.
func (c *coordinator) persistResultContent(ctx context.Context, outputRef, errorRef string, r subagents.Result, runErr *error) (string, string) {
	if outputRef != "" && len(r.Output) > 0 {
		if err := c.repo.StoreContent(ctx, outputRef, r.Output); err != nil {
			*runErr = joinError(*runErr, fmt.Errorf("store task %q output content: %w", r.TaskID, err))
			outputRef = ""
		}
	}
	if errorRef != "" && r.Err != nil {
		if err := c.repo.StoreContent(ctx, errorRef, []byte(r.Err.Error())); err != nil {
			*runErr = joinError(*runErr, fmt.Errorf("store task %q error content: %w", r.TaskID, err))
			errorRef = ""
		}
	}
	return outputRef, errorRef
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

// resultReferences mints the canonical content references for a result. All
// reference minting goes through ledger.Reference so the key the model sees is
// the key the content is stored under.
func resultReferences(r subagents.Result) (outputRef, errorRef string) {
	outputRef = ledger.Reference(ledger.RefKindOutput, r.Output)
	if r.Err != nil {
		errorRef = ledger.Reference(ledger.RefKindError, []byte(r.Err.Error()))
	}
	return outputRef, errorRef
}
