package coordinator

import (
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// retryRequeueProbeInterval bounds how often flushRetries re-probes a re-queue
// CAS that keeps failing. A failed entry is rescheduled to now plus this
// interval instead of being left with an elapsed requeueAt, so waitForRetry
// sleeps instead of returning nil immediately — an elapsed requeueAt would
// make the caller loop busy-spin flushRetries at 100% CPU against a
// persistently failing CAS. Bounded and strictly positive by construction.
const retryRequeueProbeInterval = time.Second

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
			// The task stays in the retry queue (a failed CAS must not drop or
			// re-pend it), but its entry must be rescheduled to a bounded
			// future probe time: an elapsed requeueAt left in place makes
			// waitForRetry compute sleep <= 0 and return nil immediately, so
			// the caller loop busy-spins flushRetries at 100% CPU against the
			// failing CAS. A persistent failure (e.g. ErrClaimHeld from lease
			// theft) still terminates via the heartbeat canceling poolCtx
			// within ~claimHeartbeat, after which waitForRetry's select fires
			// and the loop breaks with markCanceledWithoutResults.
			queue[taskID] = c.nowLocked().Add(retryRequeueProbeInterval)
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
			// The deferred Stop discards any fired timer value; waitForRetry
			// returns immediately after this select, so a drain is unnecessary.
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
