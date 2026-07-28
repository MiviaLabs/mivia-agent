// Package coordinator provides the orchestration seam between model-facing
// tools and the subagent execution pool. It owns orchestration policy, state
// transitions, display-name allocation, and the LedgerRepository boundary.
package coordinator

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// RunHandle is a handle to an active orchestration run. It exposes read-only
// inspection, blocking join, and cancellation. The handle is returned by Spawn
// and is safe for concurrent use.
type RunHandle struct {
	mu      sync.RWMutex
	runID   string
	done    chan struct{}
	cancel  context.CancelFunc
	poolCtx context.Context
	result  *RunResult
}

// RunResult captures the final outcome of a run.
type RunResult struct {
	Snapshot ledger.RunSnapshot
	Results  []subagents.Result
	Err      error
}

// Coordinator manages orchestration runs. It bridges synchronous Pool execution
// to an async Spawn/Inspect/Join/Cancel model backed by a LedgerRepository.
type Coordinator struct {
	repo      ledger.LedgerRepository
	pool      *subagents.Pool
	names     *ledger.DisplayNameGenerator
	handles   map[string]*RunHandle
	handlesMu sync.Mutex
	now       func() time.Time
}

// New creates a new Coordinator with the given repository and pool.
func New(repo ledger.LedgerRepository, pool *subagents.Pool) *Coordinator {
	return &Coordinator{
		repo:    repo,
		pool:    pool,
		names:   ledger.NewDisplayNameGenerator(),
		handles: map[string]*RunHandle{},
		now:     time.Now,
	}
}

// SetTimeSource replaces the clock for deterministic tests.
func (c *Coordinator) SetTimeSource(now func() time.Time) {
	c.now = now
}

// runIDCounter generates unique run IDs.
var runIDCounter atomic.Uint64

// newRunID returns a unique run identifier.
func newRunID() string {
	return fmt.Sprintf("run-%d", runIDCounter.Add(1))
}

// eventIDCounter generates unique event IDs.
var eventIDCounter atomic.Uint64

// newEventID returns a unique event identifier.
func newEventID() string {
	return fmt.Sprintf("evt-%d", eventIDCounter.Add(1))
}

// taskIDCounter generates unique task IDs.
var taskIDCounter atomic.Uint64

// newTaskID returns a unique task identifier.
func newTaskID() string {
	return fmt.Sprintf("task-%d", taskIDCounter.Add(1))
}

// Spawn creates a new orchestration run from a DAG of tasks. It validates the
// DAG, creates records in the ledger, and launches Pool.Run in a background
// goroutine. If idempotencyKey is non-empty and matches an existing run, the
// existing handle is returned.
func (c *Coordinator) Spawn(ctx context.Context, tasks []subagents.Task, idempotencyKey string) (*RunHandle, error) {
	// Check idempotency: if key matches an existing handle, return it.
	if h := c.lookupHandle(idempotencyKey); h != nil {
		return h, nil
	}

	// Validate the DAG.
	if err := c.validateTasks(tasks); err != nil {
		return nil, err
	}

	runID := newRunID()
	now := c.now()

	// Create run record.
	runSnap := ledger.RunSnapshot{
		RunID:       runID,
		DisplayName: c.names.Generate("run"),
		Status:      ledger.RunStatusCreated,
		CreatedAt:   now,
		Labels:      map[string]string{},
		Tasks:       make([]ledger.TaskSnapshot, 0, len(tasks)),
	}
	if err := c.repo.CreateRun(ctx, idempotencyKey, runSnap); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Create task records.
	namedTasks, err := c.createTasks(ctx, runID, tasks, now)
	if err != nil {
		return nil, err
	}

	// Create and register the run handle.
	h := c.newRunHandle(runID, idempotencyKey)

	// Build Pool.Run task list with ledger-assigned IDs.
	ledgerTasks := make([]subagents.Task, len(namedTasks))
	for i, nt := range namedTasks {
		ledgerTasks[i] = nt.task
		ledgerTasks[i].ID = nt.taskID
	}

	// Launch async execution.
	go c.executeRun(h, h.poolCtx, ledgerTasks)

	return h, nil
}

// lookupHandle returns the existing handle for an idempotency key, or nil.
func (c *Coordinator) lookupHandle(key string) *RunHandle {
	if key == "" {
		return nil
	}
	c.handlesMu.Lock()
	defer c.handlesMu.Unlock()
	return c.handles[key]
}

// namedTask pairs a subagents.Task with its system-assigned ledger ID and display name.
type namedTask struct {
	task        subagents.Task
	taskID      string
	displayName string
}

// createTasks creates task records in the ledger and returns them with
// system-assigned IDs and display names.
func (c *Coordinator) createTasks(ctx context.Context, runID string, tasks []subagents.Task, now time.Time) ([]namedTask, error) {
	out := make([]namedTask, 0, len(tasks))
	for _, t := range tasks {
		taskID := t.ID
		if taskID == "" {
			taskID = newTaskID()
		}
		displayName := c.names.Generate(t.Name)
		if displayName == "" {
			displayName = c.names.Generate("task")
		}
		snap := ledger.TaskSnapshot{
			RunID:        runID,
			TaskID:       taskID,
			ParentTaskID: "",
			DisplayName:  displayName,
			Status:       string(ledger.TaskStatusQueued),
			DependsOn:    make([]string, len(t.DependsOn)),
			CreatedAt:    now,
			Version:      1,
		}
		copy(snap.DependsOn, t.DependsOn)
		if err := c.repo.CreateTask(ctx, snap); err != nil {
			return nil, fmt.Errorf("create task %q: %w", taskID, err)
		}
		_ = c.repo.AppendEvent(ctx, ledger.LifecycleEvent{
			ID:     newEventID(),
			RunID:  runID,
			Kind:   "task_created",
			TaskID: taskID,
		})
		out = append(out, namedTask{task: t, taskID: taskID, displayName: displayName})
	}
	return out, nil
}

// newRunHandle creates a RunHandle and registers it in the idempotency map if
// a key is provided.
func (c *Coordinator) newRunHandle(runID, idempotencyKey string) *RunHandle {
	poolCtx, cancel := context.WithCancel(context.Background())
	h := &RunHandle{
		runID:   runID,
		done:    make(chan struct{}),
		cancel:  cancel,
		poolCtx: poolCtx,
	}
	if idempotencyKey != "" {
		c.handlesMu.Lock()
		c.handles[idempotencyKey] = h
		c.handlesMu.Unlock()
	}
	return h
}

// executeRun runs the tasks through the pool and records results in the ledger.
func (c *Coordinator) executeRun(h *RunHandle, ctx context.Context, tasks []subagents.Task) {
	defer close(h.done)

	// Update run status to running.
	if snap, err := c.repo.GetRun(ctx, h.runID); err == nil {
		snap.Status = ledger.RunStatusRunning
		_ = c.repo.CreateRun(ctx, "", snap) // best-effort update
	}

	// Mark all tasks as running.
	for _, t := range tasks {
		_, _ = c.repo.GetTask(ctx, h.runID, t.ID)
		_ = c.repo.CompareAndSetTaskStatus(ctx, h.runID, t.ID, 1, string(ledger.TaskStatusRunning))
	}

	results, runErr := c.pool.Run(ctx, tasks)

	// Record results in ledger.
	resultMap := make(map[string]subagents.Result, len(results))
	for _, r := range results {
		resultMap[r.TaskID] = r
	}

	for _, t := range tasks {
		r, ok := resultMap[t.ID]
		if !ok {
			continue
		}

		// Get current task to find version.
		taskSnap, err := c.repo.GetTask(ctx, h.runID, t.ID)
		if err != nil {
			continue
		}

		newStatus := mapStatus(r)
		if newStatus == string(ledger.TaskStatusBlocked) {
			// blocked doesn't go through CAS for version changes
			_ = c.repo.CompareAndSetTaskStatus(ctx, h.runID, t.ID, taskSnap.Version, newStatus)
		} else if err := c.repo.CompareAndSetTaskStatus(ctx, h.runID, t.ID, taskSnap.Version, newStatus); err == nil {
			// Store output refs (redacted/ bounded).
			outputRef := ""
			errorRef := ""
			if len(r.Output) > 0 {
				outputRef = fmt.Sprintf("output:%d", len(r.Output))
			}
			if r.Err != nil {
				errorRef = fmt.Sprintf("error:%s", r.Err.Error())
			}
			_ = c.repo.SetTaskOutput(ctx, h.runID, t.ID, outputRef, errorRef)
		}

		// Append lifecycle event.
		evt := ledger.LifecycleEvent{
			ID:     newEventID(),
			RunID:  h.runID,
			Kind:   "task_" + newStatus,
			TaskID: t.ID,
		}
		_ = c.repo.AppendEvent(ctx, evt)
	}

	h.mu.Lock()
	snap, _ := c.repo.GetRun(ctx, h.runID)
	h.result = &RunResult{
		Snapshot: snap,
		Results:  results,
		Err:      runErr,
	}
	h.mu.Unlock()
}

// Inspect returns a read-only snapshot of the run from the ledger.
func (c *Coordinator) Inspect(ctx context.Context, h *RunHandle) (ledger.RunSnapshot, error) {
	return c.repo.GetRun(ctx, h.runID)
}

// Join blocks until the run completes or the context is canceled. It returns
// the final RunResult.
func (c *Coordinator) Join(ctx context.Context, h *RunHandle) (*RunResult, error) {
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

// Cancel records a cancel_requested state, cancels the run context, and
// commits terminal canceled only through a valid compare-and-set transition.
func (c *Coordinator) Cancel(ctx context.Context, h *RunHandle) error {
	// First, record cancel_requested for all running tasks.
	tasks, err := c.repo.ListTasks(ctx, h.runID)
	if err != nil {
		return err
	}

	for _, t := range tasks {
		if t.Status == string(ledger.TaskStatusQueued) || t.Status == string(ledger.TaskStatusRunning) {
			_ = c.repo.CompareAndSetTaskStatus(ctx, h.runID, t.TaskID, t.Version, string(ledger.TaskStatusCancelRequested))
		}
	}

	// Cancel the pool context.
	h.cancel()

	// Wait for completion (or context cancellation) and then finalize.
	select {
	case <-h.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Finalize any remaining queued or running tasks as canceled.
	finalTasks, _ := c.repo.ListTasks(ctx, h.runID)
	for _, t := range finalTasks {
		if t.Status == string(ledger.TaskStatusQueued) || t.Status == string(ledger.TaskStatusRunning) || t.Status == string(ledger.TaskStatusCancelRequested) {
			_ = c.repo.CompareAndSetTaskStatus(ctx, h.runID, t.TaskID, t.Version, string(ledger.TaskStatusCanceled))
		}
	}

	return nil
}

// validateTasks validates a task DAG. Returns nil if valid.
func (c *Coordinator) validateTasks(tasks []subagents.Task) error {
	if len(tasks) == 0 {
		return fmt.Errorf("empty task list")
	}
	byID := map[string]bool{}
	for _, t := range tasks {
		if t.ID == "" {
			continue // will be assigned
		}
		if byID[t.ID] {
			return fmt.Errorf("duplicate task id: %s", t.ID)
		}
		byID[t.ID] = true
	}
	// Validate all dependencies exist.
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if !byID[dep] {
				return fmt.Errorf("task %q depends on unknown task %q", t.ID, dep)
			}
		}
	}
	return nil
}

// mapStatus converts a subagents result status to a ledger task status.
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
