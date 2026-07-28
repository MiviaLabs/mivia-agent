package coordinator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

var (
	errRecoveredRunNotResumable = errors.New("recovered run is nonterminal and has no live execution owner")
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
	h := c.newRunHandle(snap.RunID, key, attempts, fingerprint, true, false)
	go c.watchRecoveredRun(h)
	return h, true, nil
}

// watchRecoveredRun monitors a recovered run and resolves the handle once the
// run is terminal. Non-terminal recovered runs produce errRecoveredRunNotResumable.
func (c *coordinator) watchRecoveredRun(h *RunHandle) {
	snap, err := c.repo.GetRun(context.Background(), h.runID)
	result := &RunResult{Snapshot: snap, Err: err}
	if err == nil {
		result.Results = resultsFromSnapshots(snap.Tasks)
		if !isTerminalRunStatus(snap.Status) {
			result.Err = errRecoveredRunNotResumable
		}
	}
	h.mu.Lock()
	h.result = result
	h.mu.Unlock()
	close(h.done)
}

// ResumeInterruptedRun resumes execution of a previously interrupted run.
// It transitions running tasks to interrupted_unrecoverable (failed), then
// allows the DAG scheduler to retry them if retryPolicy is configured.
// Queued tasks are left as-is and will be picked up by the DAG execution.
// Returns a RunHandle for the resumed run.
func (c *coordinator) ResumeInterruptedRun(ctx context.Context, runID string) (*RunHandle, error) {
	// Verify the run exists and is interrupted.
	snap, err := c.repo.GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("resume: get run %q: %w", runID, err)
	}
	if isTerminalRunStatus(snap.Status) {
		return nil, fmt.Errorf("resume: run %q is already terminal (%s)", runID, snap.Status)
	}

	tasks, err := c.repo.ListTasks(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("resume: list tasks %q: %w", runID, err)
	}

	// Collect the original tasks for the DAG.
	originalTasks := make([]subagents.Task, 0, len(tasks))
	attempts := make(map[string]string, len(tasks))
	for _, task := range tasks {
		originalTasks = append(originalTasks, subagents.Task{
			ID:        task.TaskID,
			DependsOn: task.DependsOn,
		})
		if len(task.Attempts) > 0 {
			attempts[task.TaskID] = task.Attempts[len(task.Attempts)-1].AttemptID
		}
	}

	// Create a resumption handle. Recovered is false so the handle is treated
	// as a live execution (cancel works, pool context is active).
	persistCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// For each task that was running/cancel_requested at crash time, mark as
	// failed with interrupted_unrecoverable error ref.
	for _, task := range tasks {
		if task.Status != string(ledger.TaskStatusRunning) &&
			task.Status != string(ledger.TaskStatusCancelRequested) {
			continue
		}
		// CAS from the known version to failed.
		newStatus := string(ledger.TaskStatusFailed)
		if err := c.repo.CompareAndSetTaskStatus(persistCtx, runID, task.TaskID, task.Version, newStatus); err != nil {
			// If CAS fails, another goroutine may have already transitioned it.
			// That's acceptable — we'll pick up the current state.
			continue
		}
		// Record the interrupted_unrecoverable error reference.
		errorRef := "interrupted_unrecoverable"
		_ = c.repo.SetTaskOutput(persistCtx, runID, task.TaskID, "", errorRef)

		finished := c.now()
		if aid, ok := attempts[task.TaskID]; ok {
			_ = c.repo.SetTaskAttempt(persistCtx, runID, task.TaskID, aid, newStatus, &finished)
		}
		_ = c.repo.AppendEvent(persistCtx, ledger.LifecycleEvent{
			ID: newEventID(), RunID: runID, Kind: "task_interrupted_unrecoverable",
			TaskID: task.TaskID, AttemptID: attempts[task.TaskID],
		})
	}

	// Re-read tasks after marking running tasks as failed.
	updatedTasks, err := c.repo.ListTasks(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("resume: re-list tasks %q: %w", runID, err)
	}

	// Build new attempts map from updated tasks.
	newAttempts := make(map[string]string, len(updatedTasks))
	for _, task := range updatedTasks {
		if len(task.Attempts) > 0 {
			newAttempts[task.TaskID] = task.Attempts[len(task.Attempts)-1].AttemptID
		}
	}

	// Create handle for resumed execution. Allow partial results since some
	// tasks may have already completed.
	h := c.newRunHandle(runID, "", newAttempts, "", false, true)

	// Run the DAG in background, which will execute pending/retry tasks.
	go c.executeRun(h, originalTasks)

	return h, nil
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
				interrupted = append(interrupted, RecoveredRun{
					RunID:          r.RunID,
					DisplayName:    r.DisplayName,
					Status:         string(r.Status),
					WasInterrupted: r.WasInterrupted,
				})
			}
		}
		return interrupted, nil
	}
	// No recovery support (e.g. MemoryLedgerRepository) — nothing to resume.
	return nil, nil
}

// RecoveredRun is a summary of a recovered orchestration run.
type RecoveredRun struct {
	RunID          string `json:"run_id"`
	DisplayName    string `json:"display_name"`
	Status         string `json:"status"`
	WasInterrupted bool   `json:"was_interrupted"`
}

func resultsFromSnapshots(tasks []ledger.TaskSnapshot) []subagents.Result {
	results := make([]subagents.Result, len(tasks))
	for i, task := range tasks {
		results[i] = subagents.Result{
			TaskID: task.TaskID, Status: task.Status,
			Provenance: runtime.Metadata{Kind: "recovered", Status: task.Status},
		}
		if task.ErrorRef != "" {
			results[i].Err = errors.New(task.ErrorRef)
		}
	}
	return results
}

func isTerminalRunStatus(status ledger.RunStatus) bool {
	switch status {
	case ledger.RunStatusCompleted, ledger.RunStatusFailed, ledger.RunStatusCanceled:
		return true
	default:
		return false
	}
}
