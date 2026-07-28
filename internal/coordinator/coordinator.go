// Package coordinator provides the orchestration seam between model-facing
// tools and the subagent execution pool. It owns orchestration policy, state
// transitions, display-name allocation, and the LedgerRepository boundary.
package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	mu                 sync.RWMutex
	runID              string
	done               chan struct{}
	cancel             context.CancelFunc
	poolCtx            context.Context
	result             *RunResult
	attempts           map[string]string
	recovered          bool
	requestFingerprint string
	cancelOnce         sync.Once
	cancelDone         chan struct{}
	cancellationErr    error
	owner              *coordinator
	partial            bool // per-run partial results override
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
// LifecycleSubscriber receives orchestration lifecycle events in near-real-time.
type LifecycleSubscriber func(event ledger.LifecycleEvent)

// Coordinator is the public interface for orchestration runs. It bridges
// synchronous Pool execution to an async Spawn/Inspect/Join/Cancel model
// backed by a LedgerRepository.
type Coordinator interface {
	// Spawn creates a new orchestration run from a DAG of tasks.
	Spawn(ctx context.Context, tasks []subagents.Task, idempotencyKey string, partial ...bool) (*RunHandle, error)
	// Inspect returns a read-only snapshot of the run from the ledger.
	Inspect(ctx context.Context, h *RunHandle) (ledger.RunSnapshot, error)
	// Join blocks until the run completes or the context is canceled.
	Join(ctx context.Context, h *RunHandle) (*RunResult, error)
	// Cancel records cancellation and stops the run.
	Cancel(ctx context.Context, h *RunHandle) error
	// SetTimeSource replaces the clock for deterministic tests.
	SetTimeSource(now func() time.Time)
	// WithRetryPolicy enables automatic retry for failed/timed-out DAG tasks.
	WithRetryPolicy(policy RetryPolicy) Coordinator
	// ResumeInterruptedRun resumes execution of a previously interrupted run.
	ResumeInterruptedRun(ctx context.Context, runID string) (*RunHandle, error)
	// ListInterruptedRuns returns recovered runs that were interrupted.
	ListInterruptedRuns(ctx context.Context) ([]RecoveredRun, error)
	// SubscribeLifecycle registers a callback for lifecycle events. The callback
	// is called synchronously for each event emitted by the coordinator.
	// Returns an unsubscribe function. Multiple subscribers are supported.
	SubscribeLifecycle(fn LifecycleSubscriber) (unsubscribe func())
}

// coordinator manages orchestration runs. It bridges synchronous Pool execution
// to an async Spawn/Inspect/Join/Cancel model backed by a LedgerRepository.
type coordinator struct {
	repo            ledger.LedgerRepository
	pool            *subagents.Pool
	names           *ledger.DisplayNameGenerator
	handles         map[string]*RunHandle
	handlesMu       sync.Mutex
	spawnMu         sync.Mutex
	now             func() time.Time
	handleRetention time.Duration
	retryPolicy     RetryPolicy
	subscribers     []LifecycleSubscriber
	subMu           sync.RWMutex
}

// New creates a new Coordinator with the given repository and pool.
// By default, retry is disabled. Use WithRetryPolicy on the returned
// Coordinator to enable automatic retry.
func New(repo ledger.LedgerRepository, pool *subagents.Pool) Coordinator {
	return &coordinator{
		repo:            repo,
		pool:            pool,
		names:           ledger.NewDisplayNameGenerator(),
		handles:         map[string]*RunHandle{},
		now:             time.Now,
		handleRetention: 10 * time.Minute,
		retryPolicy:     NoRetry,
	}
}

// SetTimeSource replaces the clock for deterministic tests.
func (c *coordinator) SetTimeSource(now func() time.Time) {
	c.now = now
}

// WithRetryPolicy enables automatic retry for failed/timed-out DAG tasks.
// Returns the Coordinator for method chaining.
func (c *coordinator) WithRetryPolicy(policy RetryPolicy) Coordinator {
	c.retryPolicy = policy
	return c
}

// Compile-time assertion that *coordinator satisfies Coordinator.
var _ Coordinator = (*coordinator)(nil)

// SubscribeLifecycle registers a callback for lifecycle events.
// Returns an unsubscribe function. Multiple subscribers are supported.
func (c *coordinator) SubscribeLifecycle(fn LifecycleSubscriber) (unsubscribe func()) {
	if fn == nil {
		return func() {}
	}
	c.subMu.Lock()
	c.subscribers = append(c.subscribers, fn)
	c.subMu.Unlock()
	return func() {
		c.subMu.Lock()
		defer c.subMu.Unlock()
		// Search by function pointer identity and remove.
		fnPtr := fmt.Sprintf("%p", fn)
		for i := range c.subscribers {
			if fmt.Sprintf("%p", c.subscribers[i]) == fnPtr {
				c.subscribers = append(c.subscribers[:i], c.subscribers[i+1:]...)
				return
			}
		}
	}
}

// emitLifecycleEvent delivers a lifecycle event to all registered subscribers.
// Subscribers are called synchronously. Panics from subscribers are recovered
// and dropped so one bad subscriber cannot crash the coordinator.
func (c *coordinator) emitLifecycleEvent(evt ledger.LifecycleEvent) {
	c.subMu.RLock()
	safe := make([]LifecycleSubscriber, len(c.subscribers))
	copy(safe, c.subscribers)
	c.subMu.RUnlock()
	for _, fn := range safe {
		func() {
			defer func() { recover() }() //nolint:errcheck
			fn(evt)
		}()
	}
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
// existing handle is returned. The partial parameter controls whether the pool
// returns partial results on failure instead of aborting.
func (c *coordinator) Spawn(ctx context.Context, tasks []subagents.Task, idempotencyKey string, partial ...bool) (*RunHandle, error) {
	partialResults := false
	if len(partial) > 0 {
		partialResults = partial[0]
	}
	// Serialize check/create/register so concurrent retries with the same key
	// return the existing handle instead of a repository duplicate error.
	c.spawnMu.Lock()
	defer c.spawnMu.Unlock()
	fingerprint, err := requestFingerprint(tasks)
	if err != nil {
		return nil, fmt.Errorf("fingerprint spawn request: %w", err)
	}

	// Check idempotency: if key matches an existing handle, return it.
	if h := c.lookupHandle(idempotencyKey); h != nil {
		if h.requestFingerprint != fingerprint {
			return nil, ErrIdempotencyConflict
		}
		return h, nil
	}
	if idempotencyKey != "" {
		if h, found, err := c.recoverByIdempotencyKey(ctx, idempotencyKey, fingerprint); err != nil {
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
		RunID:              runID,
		DisplayName:        c.names.Generate("run"),
		Status:             ledger.RunStatusCreated,
		RequestFingerprint: fingerprint,
		CreatedAt:          now,
		Labels:             map[string]string{},
		Tasks:              make([]ledger.TaskSnapshot, 0, len(tasks)),
	}
	if err := c.repo.CreateRun(ctx, idempotencyKey, runSnap); err != nil {
		if errors.Is(err, ledger.ErrDuplicate) && idempotencyKey != "" {
			if h, found, lookupErr := c.recoverByIdempotencyKey(ctx, idempotencyKey, fingerprint); lookupErr != nil {
				return nil, lookupErr
			} else if found {
				return h, nil
			}
		}
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Emit run_created lifecycle event.
	c.emitLifecycleEvent(ledger.LifecycleEvent{
		ID: newEventID(), RunID: runID, Kind: "run_created",
	})

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
	h := c.newRunHandle(runID, idempotencyKey, attempts, fingerprint, false, partialResults)

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

var ErrIdempotencyConflict = errors.New("idempotency key already used for a different request")

func requestFingerprint(tasks []subagents.Task) (string, error) {
	payload, err := json.Marshal(tasks)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

// lookupHandle returns the existing handle for an idempotency key, or nil.
func (c *coordinator) lookupHandle(key string) *RunHandle {
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
func (c *coordinator) createTasks(ctx context.Context, runID string, tasks []subagents.Task, now time.Time) ([]namedTask, error) {
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
			HandlerName:  t.Name,
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
		c.emitLifecycleEvent(ledger.LifecycleEvent{
			ID: newEventID(), RunID: runID, Kind: "task_created", TaskID: taskID,
		})
		out = append(out, namedTask{task: t, taskID: taskID, displayName: displayName, attemptID: attemptID})
	}
	return out, nil
}

// newRunHandle creates a RunHandle. The pool context derives from the
// background so the pool can outlive the Spawn caller; Cancel is the
// explicit mechanism to stop a run.
func (c *coordinator) newRunHandle(runID, idempotencyKey string, attempts map[string]string, fingerprint string, recovered bool, partial bool) *RunHandle {
	poolCtx, cancel := context.WithCancel(context.Background())
	h := &RunHandle{
		runID:              runID,
		done:               make(chan struct{}),
		cancel:             cancel,
		poolCtx:            poolCtx,
		attempts:           attempts,
		requestFingerprint: fingerprint,
		recovered:          recovered,
		cancelDone:         make(chan struct{}),
		owner:              c,
		partial:            partial,
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
func (c *coordinator) executeRun(h *RunHandle, tasks []subagents.Task) {
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
// running before the pool has admitted it. Supports automatic retry for failed
// and timed-out tasks when retryPolicy is configured.
func (c *coordinator) runDAG(h *RunHandle, tasks []subagents.Task) ([]subagents.Result, error) {
	pending := make(map[string]subagents.Task, len(tasks))
	for _, task := range tasks {
		pending[task.ID] = task
	}
	results := make(map[string]subagents.Result, len(tasks))
	// retryQueue tracks tasks waiting for backoff before re-queue.
	// Key: taskID, Value: time when the task should be re-queued.
	retryQueue := make(map[string]time.Time)
	retryStates := make(map[string]*RetryState, len(tasks))
	var runErr error

	for len(pending) > 0 || len(retryQueue) > 0 {
		// 1. Flush expired backoffs from retryQueue back to pending.
		now := c.now()
		for taskID, requeueAt := range retryQueue {
			if now.Before(requeueAt) {
				continue
			}
			// CAS from retry_pending → queued.
			snap, err := c.repo.GetTask(h.poolCtx, h.runID, taskID)
			if err != nil {
				runErr = joinError(runErr, fmt.Errorf("read retry task %q: %w", taskID, err))
				continue
			}
			if snap.Status != string(ledger.TaskStatusRetryPending) {
				// Someone else already transitioned it; remove from queue.
				delete(retryQueue, taskID)
				continue
			}
			if err := c.repo.CompareAndSetTaskStatus(h.poolCtx, h.runID, taskID, snap.Version, string(ledger.TaskStatusQueued)); err != nil {
				runErr = joinError(runErr, fmt.Errorf("re-queue retry task %q: %w", taskID, err))
				continue
			}
			if err := c.repo.AppendEvent(h.poolCtx, ledger.LifecycleEvent{
				ID: newEventID(), RunID: h.runID, Kind: "task_retry_queued",
				TaskID: taskID, AttemptID: h.attempts[taskID],
			}); err != nil {
				runErr = joinError(runErr, fmt.Errorf("append retry event %q: %w", taskID, err))
			} else {
				c.emitLifecycleEvent(ledger.LifecycleEvent{
					ID: newEventID(), RunID: h.runID, Kind: "task_retry_queued",
					TaskID: taskID, AttemptID: h.attempts[taskID],
				})
			}
			// Re-create the original task reference so it can be picked up as ready.
			original := findTask(tasks, taskID)
			if original != nil {
				pending[taskID] = *original
			}
			delete(retryQueue, taskID)
		}

		// 2. Collect dependency-ready tasks among pending.
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

		// 3. If no ready tasks, check if we should wait for retry backoff or exit.
		if len(ready) == 0 {
			if len(retryQueue) > 0 {
				// Wait for the shortest backoff before checking again.
				nextWake := earliestRequeue(retryQueue)
				sleepDuration := time.Until(nextWake)
				if sleepDuration > 0 && sleepDuration < 30*time.Second {
					select {
					case <-h.poolCtx.Done():
						break
					case <-time.After(sleepDuration):
					}
				} else if sleepDuration > 0 {
					// Cap sleep to avoid stuck loop on weird timestamps.
					select {
					case <-h.poolCtx.Done():
						break
					case <-time.After(100 * time.Millisecond):
					}
				}
				// Also check if the run has been cancelled.
				if h.poolCtx.Err() != nil {
					runErr = joinError(runErr, h.poolCtx.Err())
					break
				}
				continue
			}
			if len(pending) > 0 {
				runErr = joinError(runErr, fmt.Errorf("dependency cycle or unresolved dependency"))
			}
			break
		}

		// 4. Transition ready tasks to running. If a task is already in a
		// retryable state (failed/timed_out from e.g. resume after crash),
		// route it through the retry pipeline instead.
		for _, task := range ready {
			if err := c.transitionTask(h, task, string(ledger.TaskStatusRunning)); err != nil {
				// Check if the task is in a retryable state and retry is configured.
				snap, getErr := c.repo.GetTask(h.poolCtx, h.runID, task.ID)
				if getErr == nil && c.retryPolicy.MaxRetries > 0 &&
					(snap.Status == string(ledger.TaskStatusFailed) || snap.Status == string(ledger.TaskStatusTimedOut)) {
					// Route through retry pipeline: failed → retry_pending → queued → running.
					if retryErr := c.transitionTaskToStatus(h, task.ID, string(ledger.TaskStatusRetryPending)); retryErr == nil {
						rs, ok := retryStates[task.ID]
						if !ok {
							rs = NewRetryState(task.ID, c.retryPolicy)
							retryStates[task.ID] = rs
						}
						backoff := rs.NextBackoff()
						requeueAt := c.now().Add(backoff)
						retryQueue[task.ID] = requeueAt
						// Placeholder result so step 5 skips this task for this batch.
						// The else-if cleanup below removes it when the task comes
						// back from the retryQueue and transitionTask succeeds.
						results[task.ID] = subagents.Result{TaskID: task.ID, Status: "retry_pending"}
						continue
					}
				}
				runErr = joinError(runErr, err)
				results[task.ID] = subagents.Result{TaskID: task.ID, Status: "failed", Err: err}
				delete(pending, task.ID)
			} else if _, isRetryPlaceholder := results[task.ID]; isRetryPlaceholder {
				// Clear any stale placeholder result from a previous retry cycle.
				delete(results, task.ID)
			}
		}

		// 5. Build batch of tasks that successfully transitioned to running.
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

		// 6. Execute the batch through the pool.
		batchResults, err := c.pool.RunWithPartial(h.poolCtx, batch, h.partial)
		runErr = joinError(runErr, err)

		// 7. Process results — handle retry for failed/timed-out tasks.
		for _, result := range batchResults {
			status := mapStatus(result)
			if c.shouldRetryTask(status, result.TaskID, retryStates) {
				// CAS to retry_pending, then schedule re-queue.
				if err := c.transitionTaskToStatus(h, result.TaskID, string(ledger.TaskStatusRetryPending)); err != nil {
					runErr = joinError(runErr, fmt.Errorf("retry_pending %q: %w", result.TaskID, err))
					results[result.TaskID] = result
					continue
				}
				rs, ok := retryStates[result.TaskID]
				if !ok {
					rs = NewRetryState(result.TaskID, c.retryPolicy)
					retryStates[result.TaskID] = rs
				}
				backoff := rs.NextBackoff()
				requeueAt := c.now().Add(backoff)
				retryQueue[result.TaskID] = requeueAt
			} else {
				// Terminal — record the result.
				results[result.TaskID] = result
			}
		}

		if h.poolCtx.Err() != nil {
			break
		}
	}

	// Mark any remaining retry-pending tasks as exhausted.
	for taskID := range retryQueue {
		if _, already := results[taskID]; !already {
			if rs, ok := retryStates[taskID]; ok {
				rs.Exhausted()
			}
			results[taskID] = subagents.Result{TaskID: taskID, Status: "failed", Err: fmt.Errorf("retry exhausted (run ended)")}
		}
		delete(retryQueue, taskID)
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

// shouldRetryTask returns true if the task result should be retried.
func (c *coordinator) shouldRetryTask(status string, taskID string, retryStates map[string]*RetryState) bool {
	if c.retryPolicy.IsZero() || c.retryPolicy.MaxRetries <= 0 {
		return false
	}
	if status != string(ledger.TaskStatusFailed) && status != string(ledger.TaskStatusTimedOut) {
		return false
	}
	rs, exists := retryStates[taskID]
	if !exists {
		return true // first failure, can retry
	}
	return rs.CanRetry()
}

// transitionTaskToStatus reads a task and CAS-es it to the given status.
func (c *coordinator) transitionTaskToStatus(h *RunHandle, taskID, status string) error {
	snap, err := c.repo.GetTask(h.poolCtx, h.runID, taskID)
	if err != nil {
		return err
	}
	if snap.Status == status {
		return nil
	}
	if err := c.repo.CompareAndSetTaskStatus(h.poolCtx, h.runID, taskID, snap.Version, status); err != nil {
		return err
	}
	evt := ledger.LifecycleEvent{
		ID: newEventID(), RunID: h.runID, Kind: "task_" + status,
		TaskID: taskID, AttemptID: h.attempts[taskID],
	}
	if err := c.repo.AppendEvent(h.poolCtx, evt); err != nil {
		return err
	}
	c.emitLifecycleEvent(evt)
	return nil
}

// findTask returns a pointer to the task with the given ID in the tasks slice.
func findTask(tasks []subagents.Task, id string) *subagents.Task {
	for i := range tasks {
		if tasks[i].ID == id {
			return &tasks[i]
		}
	}
	return nil
}

// earliestRequeue returns the earliest requeue time in the retryQueue.
func earliestRequeue(queue map[string]time.Time) time.Time {
	var earliest time.Time
	first := true
	for _, t := range queue {
		if first || t.Before(earliest) {
			earliest = t
			first = false
		}
	}
	return earliest
}

func (c *coordinator) transitionTask(h *RunHandle, task subagents.Task, status string) error {
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
	evt := ledger.LifecycleEvent{ID: newEventID(), RunID: h.runID, Kind: "task_" + status, TaskID: task.ID, AttemptID: h.attempts[task.ID]}
	if err := c.repo.AppendEvent(h.poolCtx, evt); err != nil {
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

// Join blocks until the run completes or the context is canceled. It returns
// the final RunResult.
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
