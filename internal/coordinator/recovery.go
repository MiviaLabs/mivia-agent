package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

var (
	errRecoveredRunNotResumable = errors.New("recovered run is nonterminal and has no live execution owner")
	// ErrRunHeldByAnotherExecutor is returned by ResumeInterruptedRun and
	// Spawn when the run is already claimed by another executor process.
	ErrRunHeldByAnotherExecutor = errors.New("run is held by another executor")
)

// recoverByIdempotencyKey looks up an existing run by idempotency key and
// returns a recovered handle. If the run is terminal, the handle's result is
// immediately available. If the run is non-terminal, Join/Cancel will return
// errRecoveredRunNotResumable (conservative Phase 1 behaviour).
func (c *coordinator) recoverByIdempotencyKey(ctx context.Context, key, fingerprint string) (*RunHandle, bool, error) {
	snap, err := c.repo.GetRunByIdempotencyKey(ctx, key)
	if errors.Is(err, ledger.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("recover idempotent run: %w", err)
	}
	if snap.RequestFingerprint != "" && snap.RequestFingerprint != fingerprint {
		return nil, false, ErrIdempotencyConflict
	}
	tasks, err := c.repo.ListTasks(ctx, snap.RunID)
	if err != nil {
		return nil, false, fmt.Errorf("recover idempotent tasks: %w", err)
	}
	// Defense-in-depth for D4: a run recovered in status 'created' with zero
	// tasks is an abandoned creation - a crash between the durable append and
	// the in-memory registration (or between CreateRun and the first
	// CreateTask) left a keyed run that never executed anything. Deduping onto
	// it returns a dead handle whose Join reports errRecoveredRunNotResumable,
	// permanently bricking the key, so 'created + zero tasks' is treated as
	// not-found and reclaimed (R2B-2). A running, queued or completed run with
	// tasks still dedups.
	//
	// R4-1: only the process that actually reclaims the abandoned run may
	// proceed to create. A young run is a live creator mid-creation; a failed
	// reclaim means another process is reclaiming it. Both report
	// ErrIdempotencyKeyContended so the caller's bounded retry converges onto
	// the winner's durably visible run instead of racing a second execution.
	if snap.Status == ledger.RunStatusCreated && len(tasks) == 0 {
		if time.Since(snap.CreatedAt) <= abandonedRunGracePeriod {
			return nil, false, ErrIdempotencyKeyContended
		}
		if c.reclaimAbandonedRun(snap.RunID) {
			return nil, false, nil
		}
		return nil, false, ErrIdempotencyKeyContended
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
	h := c.newRunHandle(snap.RunID, key, attempts, fingerprint, true, withRunPolicy(snap.Policy))
	go c.watchRecoveredRun(h)
	return h, true, nil
}

// ResumeInterruptedRun resumes execution of a previously interrupted run.
// It transitions running tasks to interrupted_unrecoverable (failed), then
// allows the DAG scheduler to retry them if retryPolicy is configured.
// Queued tasks are left as-is and will be picked up by the DAG execution.
// Returns a RunHandle for the resumed run.
func (c *coordinator) ResumeInterruptedRun(ctx context.Context, runID string) (*RunHandle, error) {
	return c.resumeInterruptedRun(ctx, runID, nil)
}

func (c *coordinator) resumeInterruptedRun(ctx context.Context, runID string, liveTasks []subagents.Task, opts ...runHandleOption) (*RunHandle, error) {
	c.resumeMu.Lock()
	defer c.resumeMu.Unlock()
	if c.HandleForRun(runID) != nil {
		return nil, fmt.Errorf("resume: run %q already has an execution handle", runID)
	}
	// Verify the run exists and is interrupted.
	snap, err := c.repo.GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("resume: get run %q: %w", runID, err)
	}
	if isTerminalRunStatus(snap.Status) {
		return nil, fmt.Errorf("resume: run %q is already terminal (%s)", runID, snap.Status)
	}

	// Acquire an exclusive claim BEFORE any mutation. If another executor
	// already holds the claim, refuse the resume entirely - the ledger must
	// not be touched by a process that will then refuse the run.
	if err := c.repo.ClaimRun(ctx, runID, c.holderID); err != nil {
		if errors.Is(err, ledger.ErrClaimHeld) {
			return nil, ErrRunHeldByAnotherExecutor
		}
		return nil, fmt.Errorf("resume: claim run %q: %w", runID, err)
	}
	// Deferred claim release on any error path before the DAG starts.
	// Set to false on the single successful return path.
	claimed := true
	defer func() {
		if claimed {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_ = c.repo.ReleaseRun(cleanupCtx, runID, c.holderID)
		}
	}()

	originalTasks, alreadyDone, err := c.resumeValidateAndMark(ctx, runID, liveTasks, snap.Policy.FailInterrupted)
	if err != nil {
		return nil, err
	}

	// A resumed execution is a new attempt. Reusing the interrupted attempt's ID
	// made recordRunResults overwrite it in place, so the ledger ended up showing
	// one clean attempt and no evidence the interruption ever happened.
	newAttempts := make(map[string]string, len(originalTasks))
	for _, task := range originalTasks {
		newAttempts[task.ID] = newAttemptID()
	}

	// Create handle for resumed execution. Already-completed tasks are seeded as
	// results, so the resumed run still reports one result per task.
	opts = append(opts, withRunPolicy(snap.Policy))
	h := c.newRunHandle(runID, "", newAttempts, "", false, opts...)

	// Run the DAG in background, which will execute pending/retry tasks.
	go c.executeResumedRun(h, originalTasks, alreadyDone)

	claimed = false
	return h, nil
}

// resumeValidateAndMark reads the interrupted run's tasks, validates them,
// marks any in-flight tasks as interrupted, and returns the prepared task
// list plus results from already-completed tasks.
func (c *coordinator) resumeValidateAndMark(ctx context.Context, runID string, liveTasks []subagents.Task, failInterrupted bool) ([]subagents.Task, map[string]subagents.Result, error) {
	tasks, err := c.repo.ListTasks(ctx, runID)
	if err != nil {
		return nil, nil, fmt.Errorf("resume: list tasks %q: %w", runID, err)
	}

	// Validate before mutating anything: a run that cannot be resumed must fail
	// clean, not half-marked.
	if _, _, err := c.tasksFromSnapshotsWithAuthority(ctx, tasks, liveTasks); err != nil {
		return nil, nil, err
	}
	attempts := make(map[string]string, len(tasks))
	for _, task := range tasks {
		if len(task.Attempts) > 0 {
			attempts[task.TaskID] = task.Attempts[len(task.Attempts)-1].AttemptID
		}
	}

	persistCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c.markInterruptedTasks(persistCtx, runID, tasks, attempts, failInterrupted)
	if !failInterrupted {
		c.requeuePersistedFailures(persistCtx, runID)
	}

	// Re-read tasks after marking running tasks as failed.
	updatedTasks, err := c.repo.ListTasks(ctx, runID)
	if err != nil {
		return nil, nil, fmt.Errorf("resume: re-list tasks %q: %w", runID, err)
	}

	return c.tasksFromSnapshotsWithAuthority(ctx, updatedTasks, liveTasks)
}

func (c *coordinator) markInterruptedTasks(ctx context.Context, runID string, tasks []ledger.TaskSnapshot, attempts map[string]string, failInterrupted bool) {
	for _, task := range tasks {
		if task.Status != string(ledger.TaskStatusRunning) && task.Status != string(ledger.TaskStatusCancelRequested) {
			continue
		}
		// The target depends on the source: the transition table allows
		// cancel_requested → canceled only, so aiming everything at failed left
		// a run interrupted mid-cancel stuck in cancel_requested forever -
		// never terminal, so it was reported interrupted on every startup and
		// every resume was a silent no-op.
		status := string(ledger.TaskStatusFailed)
		requeue := true
		if task.Status == string(ledger.TaskStatusCancelRequested) {
			status = string(ledger.TaskStatusCanceled)
			requeue = false // a cancellation in progress is honoured, not redone
		}
		if c.repo.CompareAndSetTaskStatus(ctx, runID, task.TaskID, task.Version, status) != nil {
			continue
		}
		_ = c.repo.SetTaskOutput(ctx, runID, task.TaskID, "", "interrupted_unrecoverable")
		finished := c.nowLocked()
		if attemptID, ok := attempts[task.TaskID]; ok {
			_ = c.repo.SetTaskAttempt(ctx, runID, task.TaskID, attemptID, status, &finished)
		}
		event := ledger.LifecycleEvent{ID: newEventID(), RunID: runID, Kind: "task_interrupted_unrecoverable", TaskID: task.TaskID, AttemptID: attempts[task.TaskID]}
		if c.repo.AppendEvent(ctx, event) == nil {
			c.emitLifecycleEvent(event)
		}
		// failed is terminal, and the DAG only revisits it when a retry policy
		// is configured - which no production path does (initCoordinator overrides
		// New's default retry policy to NoRetry, and WithRetryPolicy has no CLI caller). Without this, resume drove every
		// interrupted task to a permanent failure and the run terminal, so
		// calling resume destroyed the run instead of resuming it.
		//
		// Resume IS the retry request, so requeue here rather than depending on
		// a policy. The failed status and its event are still written first, so
		// the interruption stays in the audit trail.
		if requeue && !failInterrupted {
			c.requeueForResume(ctx, runID, task.TaskID, task.Version+1)
		}
	}
}

// requeueForResume walks failed → retry_pending → queued, the only route the
// transition table permits back to runnable (ledger/transition.go). A task
// already stranded at retry_pending (a crash or transient storage error between
// the two CASes below) is re-driven here too: it is already in the state the
// first CAS would produce, so it goes straight to queued with the live version
// (retry_pending → retry_pending is not a valid transition).
//
// Both CAS results are checked (DC-3). A first-CAS failure means the task moved
// on (a concurrent writer transitioned it, or the version went stale), so the
// second CAS is NOT written - it could overwrite the newer state. A second-CAS
// failure strands the task at retry_pending: it is logged so the stranded state
// is visible (DC-9) and requeuePersistedFailures re-drives it on the next
// resume (DC-4).
func (c *coordinator) requeueForResume(ctx context.Context, runID, taskID string, version uint64) {
	snap, err := c.repo.GetTask(ctx, runID, taskID)
	if err != nil {
		log.Printf("coordinator: resume: requeue task %q: read: %v", taskID, err)
		return
	}
	switch snap.Status {
	case string(ledger.TaskStatusRetryPending):
		// Stranded between the two CASes: already at retry_pending.
		version = snap.Version
	case string(ledger.TaskStatusFailed), string(ledger.TaskStatusTimedOut):
		if err := c.repo.CompareAndSetTaskStatus(ctx, runID, taskID, version, string(ledger.TaskStatusRetryPending)); err != nil {
			// The task moved on - do not write the second CAS.
			return
		}
		version = snap.Version + 1
	default:
		// Already queued, terminal, or cancel-claimed: nothing to re-queue.
		return
	}
	if err := c.repo.CompareAndSetTaskStatus(ctx, runID, taskID, version, string(ledger.TaskStatusQueued)); err != nil {
		// The task is stranded at retry_pending. Surface it instead of
		// silently discarding the error; requeuePersistedFailures re-drives it
		// on the next resume.
		log.Printf("coordinator: resume: requeue task %q (status %s): %v; task stranded at retry_pending, next resume re-drives it", taskID, snap.Status, err)
	}
}

// requeuePersistedFailures makes ResumeInterruptedRun the explicit retry
// boundary for work that failed just before the process stopped. A nonterminal
// run can otherwise contain failed/timed_out tasks, but the scheduler cannot
// transition either state directly back to running. A task stranded at
// retry_pending (a crash or transient error between requeueForResume's two
// CASes) is re-driven here too: retry_pending → queued is the same valid
// transition, and resume's exclusive claim prevents touching a retry owned by
// a live executor. Resume IS the retry request, so immediate re-queue
// (bypassing any remaining backoff) is correct.
func (c *coordinator) requeuePersistedFailures(ctx context.Context, runID string) {
	tasks, err := c.repo.ListTasks(ctx, runID)
	if err != nil {
		return
	}
	for _, task := range tasks {
		switch task.Status {
		case string(ledger.TaskStatusFailed), string(ledger.TaskStatusTimedOut), string(ledger.TaskStatusRetryPending):
			c.requeueForResume(ctx, runID, task.TaskID, task.Version)
		}
	}
}

// ListInterruptedRuns returns all recovered runs that were interrupted.
// These are runs with non-terminal statuses that can be resumed.
func (c *coordinator) ListInterruptedRuns(ctx context.Context) ([]RecoveredRun, error) {
	if recoverer, ok := c.repo.(interface {
		Recover(ctx context.Context) ([]ledger.RecoveredRun, error)
	}); ok {
		recovered, err := recoverer.Recover(ctx)
		if err != nil {
			return nil, err
		}
		// Filter to interrupted runs only.
		var interrupted []RecoveredRun
		for _, r := range recovered {
			if r.WasInterrupted {
				rr := RecoveredRun{
					RunID:          r.RunID,
					DisplayName:    r.DisplayName,
					Status:         string(r.Status),
					WasInterrupted: r.WasInterrupted,
					CreatedAt:      r.CreatedAt,
				}
				// Probe claim status: try to claim with our own holder. If the
				// claim succeeds (was unclaimed), release immediately. If it
				// fails with ErrClaimHeld, another executor holds the run.
				// Using c.holderID ensures no orphaned probe claims: if the
				// process crashes between ClaimRun and ReleaseRun, the stale
				// claim is our own (same as any other claim we hold).
				if err := c.repo.ClaimRun(ctx, r.RunID, c.holderID); err != nil {
					if errors.Is(err, ledger.ErrClaimHeld) {
						rr.HeldByAnotherExecutor = true
					}
				} else {
					// Probe succeeded - release the claim we just made.
					_ = c.repo.ReleaseRun(ctx, r.RunID, c.holderID)
				}
				interrupted = append(interrupted, rr)
			}
		}
		return interrupted, nil
	}
	// No recovery support (e.g. MemoryLedgerRepository) - nothing to resume.
	return nil, nil
}

// RecoveredRun is a summary of a recovered orchestration run.
type RecoveredRun struct {
	RunID          string `json:"run_id"`
	DisplayName    string `json:"display_name"`
	Status         string `json:"status"`
	WasInterrupted bool   `json:"was_interrupted"`
	// CreatedAt lets a listing show a run's age. Recover classifies a run
	// abandoned days ago identically to one interrupted moments ago, so age is
	// the only thing that distinguishes news from noise.
	CreatedAt time.Time `json:"created_at"`
	// HeldByAnotherExecutor is true when the run has an execution claim held
	// by a different repository instance (i.e. another mivia process). The
	// dashboard shows this separately so users do not try to resume it.
	HeldByAnotherExecutor bool `json:"held_by_another_executor"`
}

// ResultsFromSnapshots converts recorded task snapshots into results.
//
// Exported so a caller can salvage a run whose Join was cut short by the caller's
// own context. The run's work is recorded in the ledger, so its results stay
// recoverable even though the handle never resolved - without this, a caller whose
// budget expired reported a bare error and dropped every task that had finished.
func ResultsFromSnapshots(tasks []ledger.TaskSnapshot) []subagents.Result {
	return resultsFromSnapshots(tasks)
}

func resultsFromSnapshots(tasks []ledger.TaskSnapshot) []subagents.Result {
	results := make([]subagents.Result, len(tasks))
	for i, task := range tasks {
		results[i] = subagents.Result{
			TaskID: task.TaskID, Status: task.Status,
			Provenance: runtime.Metadata{Kind: "recovered", Status: task.Status},
		}
		// The error is gated on the STATUS, not on the presence of a reference.
		// persistResultContent blanks the ref when the content write fails, so a
		// ref-gated error let a task with Status "failed" replay with Err == nil -
		// a caller saw neither an error nor an error_ref for a task that failed.
		if isRecoveredTaskFailure(task.Status) {
			results[i].Err = recoveredTaskError(task.TaskID, task.Status, task.ErrorRef)
		}
	}
	return results
}

// recoveredTaskError describes a recovered task's failure. The reference clause
// is appended only when a reference actually exists, so the message never
// claims content that was never stored.
func recoveredTaskError(taskID, status, errorRef string) error {
	if errorRef == "" {
		return fmt.Errorf("recovered task %s: %s (no error content reference was recorded)", taskID, status)
	}
	return fmt.Errorf("recovered task %s: %s (error content reference %s)", taskID, status, errorRef)
}

// isRecoveredTaskFailure reports whether a recovered task reached a terminal
// state other than success. "completed" is the only successful terminal state;
// non-terminal states are excluded because they describe a task that never
// finished, which watchRecoveredRun reports through the run-level error instead.
//
// ledger has an equivalent unexported isTerminalTaskStatus. It stays unexported
// there - exporting a helper just to widen its reach would put a ledger
// invariant on the package's public surface - so the terminal set is restated
// here against the exported ledger.TaskStatus constants.
func isRecoveredTaskFailure(status string) bool {
	switch ledger.TaskStatus(status) {
	case ledger.TaskStatusFailed, ledger.TaskStatusTimedOut,
		ledger.TaskStatusCanceled, ledger.TaskStatusBlocked:
		return true
	default:
		return false
	}
}

func isTerminalRunStatus(status ledger.RunStatus) bool {
	switch status {
	case ledger.RunStatusCompleted, ledger.RunStatusFailed, ledger.RunStatusCanceled:
		return true
	default:
		return false
	}
}

// rebuildTasksForResume reads the run's tasks and rebuilds them for execution.
func (c *coordinator) rebuildTasksForResume(ctx context.Context, runID string) ([]subagents.Task, error) {
	snaps, err := c.repo.ListTasks(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("resume: list tasks %q: %w", runID, err)
	}
	tasks, _, err := c.tasksFromSnapshots(ctx, snaps)
	return tasks, err
}

// terminalTaskResult reports the recorded outcome of a task that has already
// finished, and whether it finished at all. A failed task is NOT terminal here:
// re-running it is the point of resuming.
func terminalTaskResult(snap ledger.TaskSnapshot) (subagents.Result, bool) {
	switch snap.Status {
	case string(ledger.TaskStatusCompleted):
		return subagents.Result{TaskID: snap.TaskID, Status: snap.Status}, true
	case string(ledger.TaskStatusCanceled), string(ledger.TaskStatusBlocked):
		// Carries an error so dependents stay blocked, as they were.
		return subagents.Result{TaskID: snap.TaskID, Status: snap.Status, Err: fmt.Errorf("task %s", snap.Status)}, true
	}
	return subagents.Result{}, false
}

// clampInt caps a persisted limit at the live ceiling. A zero ceiling means
// unconfigured, so the persisted value stands.
func clampInt(value, ceiling int) int {
	if ceiling > 0 && value > ceiling {
		return ceiling
	}
	return value
}

func clampDuration(value, ceiling, floor time.Duration) time.Duration {
	if value <= 0 && floor > 0 {
		return floor
	}
	if ceiling > 0 && value > ceiling {
		return ceiling
	}
	return value
}
