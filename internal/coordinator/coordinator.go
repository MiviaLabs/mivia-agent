// Package coordinator provides the orchestration seam between model-facing
// tools and the subagent execution pool. It owns orchestration policy, state
// transitions, display-name allocation, and the LedgerRepository boundary.
package coordinator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// executeRun runs the tasks through the pool and records results in the ledger.
func (c *coordinator) executeRun(h *RunHandle, tasks []subagents.Task) {
	c.executeResumedRun(h, tasks, nil)
}

// executeResumedRun runs tasks with the outcomes of already-finished tasks
// pre-seeded, so a dependent of a completed task can become ready without that
// task being dispatched again.
func (c *coordinator) executeResumedRun(h *RunHandle, tasks []subagents.Task, seed map[string]subagents.Result) {
	defer close(h.done)
	stopHeartbeat := c.startClaimHeartbeat(h)
	defer func() {
		stopHeartbeat()
		// Release the execution claim once the run reaches a terminal state.
		// A failed release is non-fatal: the claim will be released on Close().
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.repo.ReleaseRun(ctx, h.runID, c.holderID)
	}()
	// Ensure identity stamp is present (createAndStartRun stamps eagerly;
	// resume paths may still need the rewrite under lock).
	h.mu.Lock()
	h.poolCtx = contextWithRunExec(h.poolCtx, h.runID, tasks, h.mailboxes)
	h.mu.Unlock()
	results, runErr := c.runDAGSeeded(h, tasks, seed)
	// Wait for referral-as-spawn tasks so Join does not race claim release.
	h.waitReferrals()
	runErr = c.recordRunResults(h, tasks, results, runErr)

	h.mu.Lock()
	persistCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	snap, snapErr := c.repo.GetRun(persistCtx, h.runID)
	cancel()
	runErr = joinError(runErr, snapErr)
	h.result = &RunResult{Snapshot: snap, Results: results, Err: runErr}
	h.mu.Unlock()
}

func (c *coordinator) transitionTask(h *RunHandle, task subagents.Task, status string) error {
	if IsTaskTerminal(status) {
		h.MarkTaskMailboxTerminal(task.ID)
	}
	ctx := h.poolContext()
	snap, err := c.repo.GetTask(ctx, h.runID, task.ID)
	if err != nil {
		return fmt.Errorf("read task %q: %w", task.ID, err)
	}
	if snap.Status == status {
		return nil
	}
	if err := c.repo.CompareAndSetTaskStatus(ctx, h.runID, task.ID, snap.Version, status); err != nil {
		return fmt.Errorf("update task %q: %w", task.ID, err)
	}
	evt := ledger.LifecycleEvent{ID: newEventID(), RunID: h.runID, Kind: "task_" + status, TaskID: task.ID, AttemptID: h.getAttempt(task.ID), SessionID: task.SessionID}
	if err := c.repo.AppendEvent(ctx, evt); err != nil {
		return fmt.Errorf("append task %q event: %w", task.ID, err)
	}
	c.emitLifecycleEvent(evt)
	return nil
}

func (c *coordinator) validateHandle(h *RunHandle) error {
	if h == nil || h.owner != c {
		return fmt.Errorf("run handle does not belong to coordinator")
	}
	return nil
}

func parentTaskID(owner string) string {
	if len(owner) >= len("task-") && owner[:len("task-")] == "task-" {
		return owner
	}
	return ""
}

func joinError(current, next error) error {
	if current == nil {
		return next
	}
	if next == nil {
		return current
	}
	return errors.Join(current, next)
}

// Inspect returns a read-only snapshot of the run from the ledger.
func (c *coordinator) Inspect(ctx context.Context, h *RunHandle) (ledger.RunSnapshot, error) {
	if err := c.validateHandle(h); err != nil {
		return ledger.RunSnapshot{}, err
	}
	return c.repo.GetRun(ctx, h.runID)
}

// Join blocks until the run completes or the context is canceled.
func (c *coordinator) Join(ctx context.Context, h *RunHandle) (*RunResult, error) {
	if err := c.validateHandle(h); err != nil {
		return nil, err
	}
	select {
	case <-h.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.result == nil {
		return nil, fmt.Errorf("run completed with no result")
	}
	return h.result, nil
}

func mapStatus(r subagents.Result) string {
	switch r.Status {
	case "completed":
		return string(ledger.TaskStatusCompleted)
	case "failed":
		return string(ledger.TaskStatusFailed)
	case "timed_out":
		return string(ledger.TaskStatusTimedOut)
	case "canceled":
		return string(ledger.TaskStatusCanceled)
	case "blocked":
		return string(ledger.TaskStatusBlocked)
	default:
		if r.Err != nil {
			return string(ledger.TaskStatusFailed)
		}
		return string(ledger.TaskStatusCompleted)
	}
}
