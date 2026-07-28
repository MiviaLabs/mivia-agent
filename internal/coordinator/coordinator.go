// Package coordinator provides the orchestration seam between model-facing
// tools and the subagent execution pool. It owns orchestration policy, state
// transitions, display-name allocation, and the LedgerRepository boundary.
package coordinator

import (
	"context"
	"errors"
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
	mu       sync.RWMutex
	runID    string
	done     chan struct{}
	cancel   context.CancelFunc
	poolCtx  context.Context
	result   *RunResult
	attempts map[string]string
	owner    *Coordinator
}

// Done closes when the run reaches a terminal state.
func (h *RunHandle) Done() <-chan struct{} { return h.done }

// RunResult captures the final outcome of a run.
type RunResult struct {
	Snapshot ledger.RunSnapshot
	Results  []subagents.Result
	Err      error
}

// Coordinator manages orchestration runs. It bridges synchronous Pool execution
// to an async Spawn/Inspect/Join/Cancel model backed by a LedgerRepository.
type Coordinator struct {
	repo            ledger.LedgerRepository
	pool            *subagents.Pool
	names           *ledger.DisplayNameGenerator
	handles         map[string]*RunHandle
	handlesMu       sync.Mutex
	spawnMu         sync.Mutex
	now             func() time.Time
	handleRetention time.Duration
}

// New creates a new Coordinator with the given repository and pool.
func New(repo ledger.LedgerRepository, pool *subagents.Pool) *Coordinator {
	return &Coordinator{
		repo:            repo,
		pool:            pool,
		names:           ledger.NewDisplayNameGenerator(),
		handles:         map[string]*RunHandle{},
		now:             time.Now,
		handleRetention: 10 * time.Minute,
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

var attemptIDCounter atomic.Uint64

func newAttemptID() string {
	return fmt.Sprintf("attempt-%d", attemptIDCounter.Add(1))
}

// newTaskID returns a unique task identifier.
func newTaskID() string {
	return fmt.Sprintf("task-%d", taskIDCounter.Add(1))
}

// Spawn creates a new orchestration run from a DAG of tasks. It validates the
// DAG, creates records in the ledger, and launches Pool.Run in a background
// goroutine. If idempotencyKey is non-empty and matches an existing run, the
// existing handle is returned.
func (c *Coordinator) Spawn(ctx context.Context, tasks []subagents.Task, idempotencyKey string) (*RunHandle, error) {
	// Serialize check/create/register so concurrent retries with the same key
	// return the existing handle instead of a repository duplicate error.
	c.spawnMu.Lock()
	defer c.spawnMu.Unlock()

	// Check idempotency: if key matches an existing handle, return it.
	if h := c.lookupHandle(idempotencyKey); h != nil {
		return h, nil
	}
	if idempotencyKey != "" {
		if h, found, err := c.recoverByIdempotencyKey(ctx, idempotencyKey); err != nil {
			return nil, err
		} else if found {
			return h, nil
		}
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
		if errors.Is(err, ledger.ErrDuplicate) && idempotencyKey != "" {
			if h, found, lookupErr := c.recoverByIdempotencyKey(ctx, idempotencyKey); lookupErr != nil {
				return nil, lookupErr
			} else if found {
				return h, nil
			}
		}
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Create task records. On failure, delete the zombie run to avoid leaks.
	namedTasks, err := c.createTasks(ctx, runID, tasks, now)
	if err != nil {
		cleanupErr := c.repo.DeleteRun(ctx, runID)
		return nil, errors.Join(err, cleanupErr)
	}

	// Create and register the run handle.
	attempts := make(map[string]string, len(namedTasks))
	for _, nt := range namedTasks {
		attempts[nt.taskID] = nt.attemptID
	}
	h := c.newRunHandle(runID, idempotencyKey, attempts)

	// Build Pool.Run task list with ledger-assigned IDs.
	ledgerTasks := make([]subagents.Task, len(namedTasks))
	for i, nt := range namedTasks {
		ledgerTasks[i] = nt.task
		ledgerTasks[i].ID = nt.taskID
	}

	// Launch async execution.
	go c.executeRun(h, ledgerTasks)

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
	attemptID   string
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
			ParentTaskID: parentTaskID(t.Owner),
			DisplayName:  displayName,
			Status:       string(ledger.TaskStatusQueued),
			DependsOn:    make([]string, len(t.DependsOn)),
			CreatedAt:    now,
			Version:      1,
		}
		attemptID := newAttemptID()
		snap.Attempts = []ledger.AttemptSnapshot{{
			AttemptID:  attemptID,
			TaskID:     taskID,
			RunID:      runID,
			AttemptNum: 1,
			StartedAt:  now,
			Status:     string(ledger.TaskStatusQueued),
		}}
		copy(snap.DependsOn, t.DependsOn)
		if err := c.repo.CreateTask(ctx, snap); err != nil {
			return nil, fmt.Errorf("create task %q: %w", taskID, err)
		}
		if err := c.repo.AppendEvent(ctx, ledger.LifecycleEvent{
			ID:     newEventID(),
			RunID:  runID,
			Kind:   "task_created",
			TaskID: taskID,
		}); err != nil {
			return nil, fmt.Errorf("create task event %q: %w", taskID, err)
		}
		out = append(out, namedTask{task: t, taskID: taskID, displayName: displayName, attemptID: attemptID})
	}
	return out, nil
}

// newRunHandle creates a RunHandle. The pool context derives from the
// background so the pool can outlive the Spawn caller; Cancel is the
// explicit mechanism to stop a run.
func (c *Coordinator) newRunHandle(runID, idempotencyKey string, attempts map[string]string) *RunHandle {
	poolCtx, cancel := context.WithCancel(context.Background())
	h := &RunHandle{
		runID:    runID,
		done:     make(chan struct{}),
		cancel:   cancel,
		poolCtx:  poolCtx,
		attempts: attempts,
		owner:    c,
	}
	if idempotencyKey != "" {
		c.handlesMu.Lock()
		c.handles[idempotencyKey] = h
		c.handlesMu.Unlock()
		go c.evictHandleAfterTerminal(idempotencyKey, h)
	}
	return h
}

// executeRun runs the tasks through the pool and records results in the ledger.
func (c *Coordinator) executeRun(h *RunHandle, tasks []subagents.Task) {
	defer close(h.done)
	results, runErr := c.runDAG(h, tasks)

	runErr = c.recordRunResults(h, tasks, results, runErr)
	h.mu.Lock()
	persistCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	snap, snapErr := c.repo.GetRun(persistCtx, h.runID)
	cancel()
	runErr = joinError(runErr, snapErr)
	h.result = &RunResult{
		Snapshot: snap,
		Results:  results,
		Err:      runErr,
	}
	h.mu.Unlock()
}

// runDAG schedules only dependency-ready tasks. This keeps queued tasks queued
// until their predecessors have completed instead of claiming the whole DAG is
// running before the pool has admitted it.
func (c *Coordinator) runDAG(h *RunHandle, tasks []subagents.Task) ([]subagents.Result, error) {
	pending := make(map[string]subagents.Task, len(tasks))
	for _, task := range tasks {
		pending[task.ID] = task
	}
	results := make(map[string]subagents.Result, len(tasks))
	var runErr error
	for len(pending) > 0 {
		ready := make([]subagents.Task, 0, len(pending))
		for id, task := range pending {
			blocked := false
			isReady := true
			for _, dep := range task.DependsOn {
				result, done := results[dep]
				if !done {
					isReady = false
					continue
				}
				if result.Err != nil {
					blocked = true
				}
			}
			if blocked {
				if err := c.transitionTask(h, task, string(ledger.TaskStatusBlocked)); err != nil {
					runErr = joinError(runErr, err)
				}
				results[id] = subagents.Result{TaskID: id, Status: "blocked", Err: fmt.Errorf("dependency failed")}
				delete(pending, id)
			} else if isReady {
				ready = append(ready, task)
			}
		}
		if len(ready) == 0 {
			if len(pending) > 0 {
				runErr = joinError(runErr, fmt.Errorf("dependency cycle or unresolved dependency"))
			}
			break
		}
		for _, task := range ready {
			if err := c.transitionTask(h, task, string(ledger.TaskStatusRunning)); err != nil {
				runErr = joinError(runErr, err)
				results[task.ID] = subagents.Result{TaskID: task.ID, Status: "failed", Err: err}
				delete(pending, task.ID)
			}
		}
		batch := make([]subagents.Task, 0, len(ready))
		for _, task := range ready {
			if _, done := results[task.ID]; !done {
				task.DependsOn = nil
				batch = append(batch, task)
				delete(pending, task.ID)
			}
		}
		if len(batch) == 0 {
			continue
		}
		batchResults, err := c.pool.Run(h.poolCtx, batch)
		runErr = joinError(runErr, err)
		for _, result := range batchResults {
			results[result.TaskID] = result
		}
		if h.poolCtx.Err() != nil {
			break
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
	return out, runErr
}

func (c *Coordinator) transitionTask(h *RunHandle, task subagents.Task, status string) error {
	snap, err := c.repo.GetTask(h.poolCtx, h.runID, task.ID)
	if err != nil {
		return fmt.Errorf("read task %q: %w", task.ID, err)
	}
	if snap.Status == status {
		return nil
	}
	if err := c.repo.CompareAndSetTaskStatus(h.poolCtx, h.runID, task.ID, snap.Version, status); err != nil {
		return fmt.Errorf("update task %q: %w", task.ID, err)
	}
	if err := c.repo.AppendEvent(h.poolCtx, ledger.LifecycleEvent{ID: newEventID(), RunID: h.runID, Kind: "task_" + status, TaskID: task.ID, AttemptID: h.attempts[task.ID]}); err != nil {
		return fmt.Errorf("append task %q event: %w", task.ID, err)
	}
	return nil
}

func (c *Coordinator) validateHandle(h *RunHandle) error {
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
func (c *Coordinator) Inspect(ctx context.Context, h *RunHandle) (ledger.RunSnapshot, error) {
	if err := c.validateHandle(h); err != nil {
		return ledger.RunSnapshot{}, err
	}
	return c.repo.GetRun(ctx, h.runID)
}

// Join blocks until the run completes or the context is canceled. It returns
// the final RunResult.
func (c *Coordinator) Join(ctx context.Context, h *RunHandle) (*RunResult, error) {
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
