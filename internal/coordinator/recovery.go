package coordinator

import (
	"context"
	"encoding/json"
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
	originalTasks, err := c.tasksFromSnapshots(tasks)
	if err != nil {
		return nil, err
	}
	attempts := make(map[string]string, len(tasks))
	for _, task := range tasks {
		if len(task.Attempts) > 0 {
			attempts[task.TaskID] = task.Attempts[len(task.Attempts)-1].AttemptID
		}
	}

	// Create a resumption handle. Recovered is false so the handle is treated
	// as a live execution (cancel works, pool context is active).
	persistCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c.markInterruptedTasks(persistCtx, runID, tasks, attempts)

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

func (c *coordinator) markInterruptedTasks(ctx context.Context, runID string, tasks []ledger.TaskSnapshot, attempts map[string]string) {
	for _, task := range tasks {
		if task.Status != string(ledger.TaskStatusRunning) && task.Status != string(ledger.TaskStatusCancelRequested) {
			continue
		}
		status := string(ledger.TaskStatusFailed)
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

// rebuildTasksForResume reads the run's tasks and rebuilds them for execution.
func (c *coordinator) rebuildTasksForResume(ctx context.Context, runID string) ([]subagents.Task, error) {
	snaps, err := c.repo.ListTasks(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("resume: list tasks %q: %w", runID, err)
	}
	return c.tasksFromSnapshots(snaps)
}

// tasksFromSnapshots restores the WORK a task described and nothing else.
//
// Authority fields — Permission, Scope, Role, SessionID, TurnID, Owner — are
// left zero on purpose. The ledger is a file in the workspace and the agent can
// write it, so restoring a persisted permission would let the agent grant
// itself one; those are re-derived by the caller performing the resume.
// Idempotency keys are likewise not restored: reusing a persisted key would
// make the resumed attempt dedupe against the original and never run.
//
// Limits are restored but clamped to the live pool policy, so a run that
// predates a config change keeps its smaller budget while a ledger claiming a
// larger one cannot raise the ceiling. See plan 12 §3.
func (c *coordinator) tasksFromSnapshots(snaps []ledger.TaskSnapshot) ([]subagents.Task, error) {
	out := make([]subagents.Task, 0, len(snaps))
	for _, snap := range snaps {
		if snap.HandlerName == "" {
			return nil, fmt.Errorf("resume: task %q has no handler name (created by an older mivia version; cannot dispatch)", snap.TaskID)
		}
		if len(snap.Input) == 0 {
			return nil, fmt.Errorf("resume: task %q has no persisted input (created before task inputs were recorded; cannot resume this run)", snap.TaskID)
		}
		out = append(out, subagents.Task{
			ID:        snap.TaskID,
			Name:      snap.HandlerName,
			DependsOn: snap.DependsOn,
			Input:     append(json.RawMessage(nil), snap.Input...),
			Depth:     clampInt(snap.Depth, c.pool.MaxDepth()),
			Budget:    clampInt(snap.Budget, c.pool.MaxBudget()),
			Timeout:   clampDuration(snap.Timeout, c.pool.Timeout()),
		})
	}
	return out, nil
}

// clampInt caps a persisted limit at the live ceiling. A zero ceiling means
// unconfigured, so the persisted value stands.
func clampInt(value, ceiling int) int {
	if ceiling > 0 && value > ceiling {
		return ceiling
	}
	return value
}

func clampDuration(value, ceiling time.Duration) time.Duration {
	if ceiling > 0 && value > ceiling {
		return ceiling
	}
	return value
}
