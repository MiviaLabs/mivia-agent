package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func (c *coordinator) recordRunResults(h *RunHandle, tasks []subagents.Task, results []subagents.Result, runErr error) error {
	// Early-CAS window (plan R9): Pool.OnTaskDone may have already CASed a
	// task to a terminal status (and fenced its mailbox) while the pool is
	// still running. A crash between that early CAS and this finalize leaves a
	// terminal task with no output ref — a pre-existing window; nothing in the
	// DAG reads output, and resume seeds such tasks as done.
	persistCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Record results in ledger.
	resultMap := make(map[string]subagents.Result, len(results))
	for _, r := range results {
		resultMap[r.TaskID] = r
	}

	for i, t := range tasks {
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

		// A task already claimed for cancellation (cancel_requested or canceled)
		// must finalize as canceled, never as the stale pool outcome. This is the
		// recordRunResults side of the cancel/startReady race: the pool produced a
		// result, then reconcileCancellation's running->cancel_requested CAS won,
		// so a CAS to completed/failed would be an invalid transition. Override
		// both the result surface and the ledger so they agree on a clean cancel.
		if taskSnap.Status == string(ledger.TaskStatusCancelRequested) || taskSnap.Status == string(ledger.TaskStatusCanceled) {
			r.Status = "canceled"
			r.Err = h.poolCtx.Err()
			if r.Err == nil {
				r.Err = context.Canceled
			}
			resultMap[t.ID] = r
			results[i] = r
		}

		newStatus := mapStatus(r)
		casOK := c.tryTaskStatusCAS(persistCtx, h.runID, t.ID, taskSnap, newStatus, &runErr)

		if casOK {
			// Terminal mailbox fence (plan 53.03): reject further sends without
			// close-on-terminal. Most terminals land here, not via transitionTask.
			if IsTaskTerminal(newStatus) {
				h.MarkTaskMailboxTerminal(t.ID)
			}
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
