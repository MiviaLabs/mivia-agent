package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func (c *coordinator) runDAG(h *RunHandle, tasks []subagents.Task) ([]subagents.Result, error) {
	return c.runDAGSeeded(h, tasks, nil)
}

func (c *coordinator) runDAGSeeded(h *RunHandle, tasks []subagents.Task, seed map[string]subagents.Result) ([]subagents.Result, error) {
	pending := make(map[string]subagents.Task, len(tasks))
	for _, task := range tasks {
		pending[task.ID] = task
	}
	results := make(map[string]subagents.Result, len(tasks)+len(seed))
	for id, result := range seed {
		results[id] = result
	}
	retryQueue := make(map[string]time.Time)
	retryStates := make(map[string]*RetryState, len(tasks))
	var runErr error
	for len(pending) > 0 || len(retryQueue) > 0 {
		runErr = joinError(runErr, c.flushRetries(h, tasks, pending, retryQueue))
		ready, err := c.collectReady(h, pending, results)
		runErr = joinError(runErr, err)
		if len(ready) == 0 {
			if len(retryQueue) == 0 {
				if len(pending) > 0 {
					runErr = joinError(runErr, fmt.Errorf("dependency cycle or unresolved dependency"))
				}
				break
			}
			if err := waitForRetry(h, retryQueue); err != nil {
				runErr = joinError(runErr, err)
				if h.poolCtx.Err() != nil {
					// Canceled while waiting out a retry backoff. Tasks still in the
					// retry queue (retry_pending in the ledger) were never dispatched
					// again, so mark them canceled instead of letting finalizeDAG emit
					// "retry exhausted (run ended)" and recordRunResults attempt a
					// forbidden retry_pending->failed CAS.
					markCanceledWithoutResults(h, tasks, results)
				}
				break
			}
			continue
		}
		runErr = joinError(runErr, c.startReady(h, ready, pending, results, retryQueue, retryStates))
		batch := buildBatch(ready, pending, results, retryQueue)
		if len(batch) == 0 {
			continue
		}
		batchResults, err := c.pool.Run(h.poolCtx, batch)
		runErr = joinError(runErr, err)
		runErr = joinError(runErr, c.processResults(h, batchResults, results, retryQueue, retryStates))
		if h.poolCtx.Err() != nil {
			// The run is being canceled. Tasks that never reached the pool have
			// no result, so finalizeDAG would otherwise emit "missing" and
			// recordRunResults would try an invalid queued->completed CAS. Mark
			// them canceled (a valid target from queued or cancel_requested) so
			// both the result set and the ledger agree on a clean cancel.
			markCanceledWithoutResults(h, tasks, results)
			break
		}
	}
	return c.finalizeDAG(tasks, results, retryQueue, retryStates), runErr
}

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
		// Mint a fresh attempt identity for this retry so per-attempt
		// telemetry is distinct across attempts.
		runErr = joinError(runErr, c.mintRetryAttempt(h, taskID))
		event := ledger.LifecycleEvent{ID: newEventID(), RunID: h.runID, Kind: "task_retry_queued", TaskID: taskID, AttemptID: h.getAttempt(taskID)}
		if err := c.repo.AppendEvent(h.poolCtx, event); err != nil {
			runErr = joinError(runErr, fmt.Errorf("append retry event %q: %w", taskID, err))
		} else {
			c.emitLifecycleEvent(event)
		}
		if original := findTask(tasks, taskID); original != nil {
			pending[taskID] = *original
		}
		delete(queue, taskID)
	}
	return runErr
}

func (c *coordinator) collectReady(h *RunHandle, pending map[string]subagents.Task, results map[string]subagents.Result) ([]subagents.Task, error) {
	ready := make([]subagents.Task, 0, len(pending))
	var runErr error
	for id, task := range pending {
		blockedBy, isReady := "", true
		for _, dep := range task.DependsOn {
			result, done := results[dep]
			if !done {
				isReady = false
				continue
			}
			if result.Err != nil {
				blockedBy = dep
			}
		}
		if blockedBy != "" {
			// Transition first. Returning before this - as the pool's own ready()
			// does - would leave the task queued forever, and tasksFromSnapshots
			// would re-dispatch it on the next resume.
			if err := c.transitionTask(h, task, string(ledger.TaskStatusBlocked)); err != nil {
				runErr = joinError(runErr, err)
			}
			results[id] = subagents.Result{
				TaskID: id, Status: "blocked",
				Err: fmt.Errorf("dependency %s failed", blockedBy),
			}
			delete(pending, id)
			// Always a run-level failure. The full result set is returned regardless,
			// so reporting this costs the caller nothing and withholding it left a run
			// that silently did less than it was asked to look like a clean success.
			runErr = joinError(runErr, fmt.Errorf("task %s blocked: dependency %s failed", id, blockedBy))
		} else if isReady {
			ready = append(ready, task)
		}
	}
	return ready, runErr
}

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

func (c *coordinator) startReady(h *RunHandle, ready []subagents.Task, pending map[string]subagents.Task, results map[string]subagents.Result, queue map[string]time.Time, states map[string]*RetryState) error {
	var runErr error
	for _, task := range ready {
		if err := c.transitionTask(h, task, string(ledger.TaskStatusRunning)); err == nil {
			continue
		} else if c.queueRecoveredRetry(h, task, pending, queue, states) {
			continue
		} else if c.isCancelClaimed(h, task.ID) {
			// The dispatch CAS lost a race against reconcileCancellation: the
			// task is already cancel_requested/canceled. Surface it as canceled
			// rather than failed, and do not join the invalid-transition
			// artifact into the run error.
			cancelErr := h.poolCtx.Err()
			if cancelErr == nil {
				cancelErr = context.Canceled
			}
			results[task.ID] = subagents.Result{TaskID: task.ID, Status: "canceled", Err: cancelErr}
			delete(pending, task.ID)
		} else {
			runErr = joinError(runErr, err)
			results[task.ID] = subagents.Result{TaskID: task.ID, Status: "failed", Err: err}
			delete(pending, task.ID)
		}
	}
	return runErr
}

// isCancelClaimed reports whether the task's current ledger status shows it has
// already been claimed for cancellation (cancel_requested or canceled). When a
// startReady dispatch CAS loses to reconcileCancellation, this distinguishes a
// cancellation race from a genuine failure so the task surfaces as canceled.
func (c *coordinator) isCancelClaimed(h *RunHandle, taskID string) bool {
	snap, err := c.repo.GetTask(context.Background(), h.runID, taskID)
	if err != nil {
		return false
	}
	return snap.Status == string(ledger.TaskStatusCancelRequested) || snap.Status == string(ledger.TaskStatusCanceled)
}

// markCanceledWithoutResults emits a canceled result for every task on a run
// being canceled mid-flight, so finalizeDAG never emits "missing" or "retry
// exhausted (run ended)" and recordRunResults transitions each task cleanly to
// canceled. Tasks that already produced a non-terminal result (failed,
// timed_out, retry_pending) are overwritten too: a task that was about to be
// retried when the run was aborted is not a terminal outcome of a canceled run.
func markCanceledWithoutResults(h *RunHandle, tasks []subagents.Task, results map[string]subagents.Result) {
	for _, task := range tasks {
		if h.poolCtx.Err() == nil {
			continue
		}
		if result, ok := results[task.ID]; ok {
			switch result.Status {
			case "failed", "timed_out", "retry_pending":
				results[task.ID] = canceledResult(h, task.ID)
			}
			continue
		}
		results[task.ID] = canceledResult(h, task.ID)
	}
}

// canceledResult builds the canceled result used when a run is canceled while a
// task is mid-flight. It carries the run's cancellation error when available,
// falling back to context.Canceled for the window where the ledger has already
// claimed the task for cancellation but poolCtx has not been canceled yet.
func canceledResult(h *RunHandle, taskID string) subagents.Result {
	cancelErr := h.poolCtx.Err()
	if cancelErr == nil {
		cancelErr = context.Canceled
	}
	return subagents.Result{TaskID: taskID, Status: "canceled", Err: cancelErr}
}

func (c *coordinator) queueRecoveredRetry(h *RunHandle, task subagents.Task, pending map[string]subagents.Task, queue map[string]time.Time, states map[string]*RetryState) bool {
	snap, err := c.repo.GetTask(h.poolCtx, h.runID, task.ID)
	if err != nil || c.retryPolicyLocked().MaxRetries <= 0 || (snap.Status != string(ledger.TaskStatusFailed) && snap.Status != string(ledger.TaskStatusTimedOut)) {
		return false
	}
	if c.transitionTaskToStatus(h, task.ID, string(ledger.TaskStatusRetryPending)) != nil {
		return false
	}
	state := retryState(task.ID, states, c.retryPolicyLocked())
	queue[task.ID] = c.nowLocked().Add(state.NextBackoff())
	delete(pending, task.ID)
	return true
}

func buildBatch(ready []subagents.Task, pending map[string]subagents.Task, results map[string]subagents.Result, queue map[string]time.Time) []subagents.Task {
	batch := make([]subagents.Task, 0, len(ready))
	for _, task := range ready {
		if _, done := results[task.ID]; done {
			continue
		}
		if _, retrying := queue[task.ID]; retrying {
			continue
		}
		task.DependsOn = nil
		batch = append(batch, task)
		delete(pending, task.ID)
	}
	return batch
}

func (c *coordinator) processResults(h *RunHandle, batch []subagents.Result, results map[string]subagents.Result, queue map[string]time.Time, states map[string]*RetryState) error {
	var runErr error
	for _, result := range batch {
		if !c.shouldRetryTask(mapStatus(result), result.TaskID, states) {
			results[result.TaskID] = result
			continue
		}
		// A cancel racing a genuine failure must not be retried. If the run is
		// being canceled or the task is already claimed for cancellation
		// (reconcileCancellation CASed running -> cancel_requested while the
		// pool computed "failed"), a retry transition would lose the CAS and
		// pollute the run error with an invalid-transition artifact. Surface
		// the task as canceled instead of attempting the retry transition.
		if h.poolCtx.Err() != nil || c.isCancelClaimed(h, result.TaskID) {
			results[result.TaskID] = canceledResult(h, result.TaskID)
			continue
		}
		if err := c.transitionTaskToStatus(h, result.TaskID, string(ledger.TaskStatusRetryPending)); err != nil {
			// The retry CAS can still lose to reconcileCancellation between the
			// isCancelClaimed check above and this CAS. Re-check before treating
			// it as a genuine error: a cancel-claimed task surfaces as canceled
			// and joins no spurious transition error into the run.
			if c.isCancelClaimed(h, result.TaskID) {
				results[result.TaskID] = canceledResult(h, result.TaskID)
				continue
			}
			runErr = joinError(runErr, fmt.Errorf("retry_pending %q: %w", result.TaskID, err))
			results[result.TaskID] = result
			continue
		}
		runErr = joinError(runErr, c.mintRetryAttempt(h, result.TaskID))
		state := retryState(result.TaskID, states, c.retryPolicyLocked())
		queue[result.TaskID] = c.nowLocked().Add(state.NextBackoff())
	}
	return runErr
}

func retryState(taskID string, states map[string]*RetryState, policy RetryPolicy) *RetryState {
	if state := states[taskID]; state != nil {
		return state
	}
	state := NewRetryState(taskID, policy)
	states[taskID] = state
	return state
}

func (c *coordinator) finalizeDAG(tasks []subagents.Task, results map[string]subagents.Result, queue map[string]time.Time, states map[string]*RetryState) []subagents.Result {
	for taskID := range queue {
		if _, ok := results[taskID]; !ok {
			if state := states[taskID]; state != nil {
				state.Exhausted()
			}
			results[taskID] = subagents.Result{TaskID: taskID, Status: "failed", Err: fmt.Errorf("retry exhausted (run ended)")}
		}
	}
	out := make([]subagents.Result, 0, len(tasks))
	for _, task := range tasks {
		if result, ok := results[task.ID]; ok {
			out = append(out, result)
		} else {
			out = append(out, subagents.Result{TaskID: task.ID, Status: "missing"})
		}
	}
	return out
}

func (c *coordinator) shouldRetryTask(status, taskID string, states map[string]*RetryState) bool {
	if c.retryPolicyLocked().IsZero() || c.retryPolicyLocked().MaxRetries <= 0 || (status != string(ledger.TaskStatusFailed) && status != string(ledger.TaskStatusTimedOut)) {
		return false
	}
	state := states[taskID]
	return state == nil || state.CanRetry()
}

func (c *coordinator) transitionTaskToStatus(h *RunHandle, taskID, status string) error {
	ctx := h.poolContext()
	snap, err := c.repo.GetTask(ctx, h.runID, taskID)
	if err != nil {
		return err
	}
	if snap.Status == status {
		return nil
	}
	if err := c.repo.CompareAndSetTaskStatus(ctx, h.runID, taskID, snap.Version, status); err != nil {
		return err
	}
	event := ledger.LifecycleEvent{ID: newEventID(), RunID: h.runID, Kind: "task_" + status, TaskID: taskID, AttemptID: h.getAttempt(taskID)}
	if err := c.repo.AppendEvent(ctx, event); err != nil {
		return err
	}
	c.emitLifecycleEvent(event)
	return nil
}

// mintRetryAttempt creates a fresh attempt ID for a retry, updates the
// run handle's attempt map, and records the new attempt in the ledger.
// Each retry gets its own AttemptID so per-attempt telemetry is distinct.
// The per-attempt ask quota is also reset here (FIX R6): a retried task must
// get a fresh ask budget for its new attempt instead of inheriting attempt 1's
// count forever. Open/closed/claimed ask bookkeeping is untouched — in-flight
// open asks are retired at the attempt boundary via CloseAsk/SealAskAnswer.
func (c *coordinator) mintRetryAttempt(h *RunHandle, taskID string) error {
	attemptID := newAttemptID()
	h.setAttempt(taskID, attemptID)
	c.resetTaskAsks(h.runID, taskID)
	now := c.nowLocked()
	if err := c.repo.SetTaskAttempt(h.poolContext(), h.runID, taskID, attemptID, string(ledger.TaskStatusQueued), &now); err != nil {
		return fmt.Errorf("record retry attempt %q: %w", taskID, err)
	}
	return nil
}

func findTask(tasks []subagents.Task, id string) *subagents.Task {
	for i := range tasks {
		if tasks[i].ID == id {
			return &tasks[i]
		}
	}
	return nil
}
func earliestRequeue(queue map[string]time.Time) time.Time {
	var earliest time.Time
	for _, value := range queue {
		if earliest.IsZero() || value.Before(earliest) {
			earliest = value
		}
	}
	return earliest
}
