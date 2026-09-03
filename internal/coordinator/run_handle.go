package coordinator

import (
	"context"
)

func (h *RunHandle) policy() RetryPolicy {
	if h == nil {
		return NoRetry
	}
	h.mu.RLock()
	p := h.retryPolicy
	h.mu.RUnlock()
	return p
}

func (h *RunHandle) mustFailInterrupted() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	v := h.failInterrupted
	h.mu.RUnlock()
	return v
}

func (h *RunHandle) Done() <-chan struct{} { return h.done }

// TaskProgress returns the live per-task tool-call liveness view for
// parent-facing inspection (inspect_agents): tool-call counts and
// last-activity stamps that survive the raw trace buffer's caps, so a chatty
// task never reads stale. Nil-receiver safe; nil when nothing has run.
func (h *RunHandle) TaskProgress() map[string]TaskProgress {
	if h == nil {
		return nil
	}
	return h.toolCalls.progressSnapshot()
}

// LocalActor reports whether this process owns execution of the run.
func (h *RunHandle) LocalActor() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	local := h.localActor
	h.mu.RUnlock()
	return local
}

// isNonInteractiveParent reports whether the run's parent cannot answer child
// questions. Locking accessor: the flag is written at construction before the
// run goroutine starts and never mutated, but ParkQuestion may be reached from
// any pool worker, so reads go through h.mu like poolContext().
func (h *RunHandle) isNonInteractiveParent() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.nonInteractiveParent
}

// poolContext returns the run's pool context under lock so concurrent
// referral tasks do not race executeResumedRun's rewrite of poolCtx.
func (h *RunHandle) poolContext() context.Context {
	if h == nil {
		return context.Background()
	}
	h.mu.RLock()
	ctx := h.poolCtx
	h.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// setAttempt records the current attempt ID for a task. Must be called from
// the single writer goroutine (DAG execution). Concurrent with getAttempt
// from the cancel goroutine.
func (h *RunHandle) setAttempt(taskID, attemptID string) {
	h.attemptsMu.Lock()
	h.attempts[taskID] = attemptID
	h.attemptsMu.Unlock()
}

// getAttempt returns the current attempt ID for a task. Safe for concurrent
// use from any goroutine.
func (h *RunHandle) getAttempt(taskID string) string {
	h.attemptsMu.RLock()
	v := h.attempts[taskID]
	h.attemptsMu.RUnlock()
	return v
}

// registerTaskCancel installs the CancelFunc for a task's own dispatch-scoped
// execution context (called from onTaskStart on the pool worker goroutine,
// once per dispatch attempt) and returns a fresh completion channel that
// signalTaskDone closes when that same attempt's executeOne call returns.
// Safe for concurrent registration across sibling tasks.
//
// Retry caveat: each dispatch attempt overwrites the prior entry, so in the
// abstract a CancelTask call that reads an attempt's CancelFunc/done channel
// just before a concurrent RETRY re-registers a fresh attempt would
// cancel/wait-on the stale attempt and then finalize the task as canceled
// while a new attempt ran.
//
// That cannot happen, and NOT because retries are off: [subagents.retry]
// max_retries is a user-facing setting that merely DEFAULTS to 0, and
// internal/cliorchestrate wires whatever it is set to
// (TaskRetryPolicyFromConfig in orchestration_state.go). The real fence is
// ORDERING plus processResults (dag.go). requestSingleTaskCancel durably
// CASes the task to cancel_requested BEFORE CancelTask reads taskCancelFunc,
// so attempt 1's failure reaches processResults with isCancelClaimed already
// true: the task surfaces as canceled and no second attempt is minted.
// flushRetries likewise drops queue entries that left retry_pending.
// Verified with MaxRetries: 2 (attempts=1, ledger "canceled"). Keep that
// ordering if this code is restructured.
func (h *RunHandle) registerTaskCancel(taskID string, cancel context.CancelFunc) chan struct{} {
	done := make(chan struct{})
	h.taskCancelMu.Lock()
	if h.taskCancels == nil {
		h.taskCancels = map[string]context.CancelFunc{}
	}
	if h.taskDoneCh == nil {
		h.taskDoneCh = map[string]chan struct{}{}
	}
	h.taskCancels[taskID] = cancel
	h.taskDoneCh[taskID] = done
	h.taskCancelMu.Unlock()
	return done
}

// taskCancelFunc returns the current CancelFunc and completion channel for a
// dispatched task, if any is registered. ok is false when the task has never
// been dispatched to the pool (e.g. it is still queued): CancelTask cancels
// such a task through the ledger CAS alone, with nothing to invoke here.
func (h *RunHandle) taskCancelFunc(taskID string) (cancel context.CancelFunc, done chan struct{}, ok bool) {
	h.taskCancelMu.RLock()
	cancel, ok = h.taskCancels[taskID]
	done = h.taskDoneCh[taskID]
	h.taskCancelMu.RUnlock()
	return cancel, done, ok
}

// signalTaskDone closes the current dispatch attempt's completion channel for
// a task, if one is registered. Each registerTaskCancel call installs a fresh
// channel, so this only ever closes the channel belonging to the attempt that
// just finished; a task never dispatched (no registration) is a no-op.
func (h *RunHandle) signalTaskDone(taskID string) {
	h.taskCancelMu.Lock()
	if done, ok := h.taskDoneCh[taskID]; ok {
		select {
		case <-done:
		default:
			close(done)
		}
	}
	h.taskCancelMu.Unlock()
}
