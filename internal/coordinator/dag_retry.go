package coordinator

import (
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// flushRetries re-queues every retry task whose backoff elapsed: the ledger
// moves retry_pending -> queued, the task_retry_queued event is appended and
// emitted, and the task re-enters the pending set so collectReady can pick it
// up again.
func (c *coordinator) flushRetries(h *RunHandle, tasks []subagents.Task, pending map[string]subagents.Task, queue map[string]time.Time) error {
	var runErr error
	for taskID, requeueAt := range queue {
		if c.nowLocked().Before(requeueAt) {
			continue
		}
		snap, err := c.repo.GetTask(h.poolCtx, h.runID, taskID)
		if err != nil {
			runErr = joinError(runErr, fmt.Errorf("read retry task %q: %w", taskID, err))
			continue
		}
		if snap.Status != string(ledger.TaskStatusRetryPending) {
			delete(queue, taskID)
			continue
		}
		if err := c.repo.CompareAndSetTaskStatus(h.poolCtx, h.runID, taskID, snap.Version, string(ledger.TaskStatusQueued)); err != nil {
			runErr = joinError(runErr, fmt.Errorf("re-queue retry task %q: %w", taskID, err))
			continue
		}
		// The retry attempt was already minted by processResults when the task
		// was moved to retry_pending. Do not mint a second attempt here — the
		// processResults mintRetryAttempt is the single source of retry attempt
		// identity and quota reset.
		original := findTask(tasks, taskID)
		var sessionID string
		if original != nil {
			sessionID = original.SessionID
		}
		event := ledger.LifecycleEvent{ID: newEventID(), RunID: h.runID, Kind: "task_retry_queued", TaskID: taskID, AttemptID: h.getAttempt(taskID), SessionID: sessionID}
		if err := c.repo.AppendEvent(h.poolCtx, event); err != nil {
			runErr = joinError(runErr, fmt.Errorf("append retry event %q: %w", taskID, err))
		} else {
			c.emitLifecycleEvent(event)
		}
		if original != nil {
			pending[taskID] = *original
		}
		delete(queue, taskID)
	}
	return runErr
}

// waitForRetry sleeps until the earliest retry requeue time, or until the run
// context is canceled. A canceled wait returns the context error so the caller
// stops dispatching.
func waitForRetry(h *RunHandle, queue map[string]time.Time) error {
	sleep := time.Until(earliestRequeue(queue))
	if sleep > 0 {
		timer := time.NewTimer(sleep)
		defer timer.Stop()
		select {
		case <-h.poolCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
	return h.poolCtx.Err()
}

// earliestRequeue returns the soonest requeue time in the retry queue, or the
// zero time when the queue is empty.
func earliestRequeue(queue map[string]time.Time) time.Time {
	var earliest time.Time
	for _, value := range queue {
		if earliest.IsZero() || value.Before(earliest) {
			earliest = value
		}
	}
	return earliest
}
