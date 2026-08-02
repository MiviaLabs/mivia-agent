package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func (c *coordinator) Spawn(ctx context.Context, tasks []subagents.Task, idempotencyKey string) (*RunHandle, error) {
	c.spawnMu.Lock()
	defer c.spawnMu.Unlock()
	fingerprint, err := requestFingerprint(tasks)
	if err != nil {
		return nil, fmt.Errorf("fingerprint spawn request: %w", err)
	}
	key := scopedKey(ctx, idempotencyKey)
	if h := c.lookupHandle(key); h != nil {
		if h.requestFingerprint != fingerprint {
			return nil, ErrIdempotencyConflict
		}
		return h, nil
	}
	if key != "" {
		h, found, err := c.recoverIdempotentWithRetry(ctx, key, fingerprint)
		if err != nil {
			return nil, err
		}
		if found {
			return h, nil
		}
	}
	if err := c.validateTasks(tasks); err != nil {
		return nil, err
	}
	return c.createAndStartRun(ctx, tasks, key, fingerprint)
}

func (c *coordinator) createAndStartRun(ctx context.Context, tasks []subagents.Task, key, fingerprint string) (*RunHandle, error) {
	runID, now := newRunID(), c.nowLocked()
	run := ledger.RunSnapshot{RunID: runID, DisplayName: c.names.Generate("run"), Status: ledger.RunStatusCreated, RequestFingerprint: fingerprint, CreatedAt: now, Labels: map[string]string{}, Tasks: make([]ledger.TaskSnapshot, 0, len(tasks))}
	if err := c.repo.CreateRun(ctx, key, run); err != nil {
		if errors.Is(err, ledger.ErrDuplicate) && key != "" {
			h, found, lookupErr := c.recoverIdempotentWithRetry(ctx, key, fingerprint)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if found {
				return h, nil
			}
		}
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Acquire an exclusive execution claim on this run before any further
	// mutations. If another executor already holds a claim, refuse.
	if err := c.repo.ClaimRun(ctx, runID, c.holderID); err != nil {
		if errors.Is(err, ledger.ErrClaimHeld) {
			// Clean up the run we just created.
			_ = c.repo.DeleteRun(ctx, runID)
			return nil, ErrRunHeldByAnotherExecutor
		}
		_ = c.repo.DeleteRun(ctx, runID)
		return nil, fmt.Errorf("claim run %q: %w", runID, err)
	}

	event := ledger.LifecycleEvent{ID: newEventID(), RunID: runID, Kind: "run_created"}
	if err := c.repo.AppendEvent(ctx, event); err != nil {
		c.releaseAndDeleteRun(ctx, runID)
		return nil, fmt.Errorf("append run_created event: %w", err)
	}
	c.emitLifecycleEvent(event)
	named, err := c.createTasks(ctx, runID, tasks, now)
	if err != nil {
		c.releaseAndDeleteRun(ctx, runID)
		return nil, fmt.Errorf("create tasks: %w", err)
	}
	attempts := make(map[string]string, len(named))
	ledgerTasks := make([]subagents.Task, len(named))
	for i, task := range named {
		attempts[task.taskID] = task.attemptID
		ledgerTasks[i] = task.task
		ledgerTasks[i].ID = task.taskID
	}
	h := c.newRunHandle(runID, key, attempts, fingerprint, false)
	go c.executeRun(h, ledgerTasks)
	return h, nil
}

var ErrIdempotencyConflict = errors.New("idempotency key already used for a different request")

// fingerprintTask is the explicit list of fields that describe the requested
// work. It is a projection of subagents.Task, not the struct itself: caller
// identity is deliberately excluded because it is idempotency-key scope, not
// requested work. Add new work-defining Task fields here deliberately.
type fingerprintTask struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	DependsOn    []string        `json:"depends_on,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	Timeout      time.Duration   `json:"timeout,omitempty"`
	Budget       int             `json:"budget,omitempty"`
	Scope        string          `json:"scope,omitempty"`
	Permission   string          `json:"permission,omitempty"`
	AgentName    string          `json:"agent_name"`
	AgentDigest  string          `json:"agent_digest"`
	Skill        string          `json:"skill,omitempty"`
	ProviderName string          `json:"provider_name,omitempty"`
	Model        string          `json:"model,omitempty"`
	OutputSchema map[string]any  `json:"output_schema,omitempty"`
	InputSchema  map[string]any  `json:"input_schema,omitempty"`
}

// requestFingerprint returns the canonical identity of the work in tasks.
func requestFingerprint(tasks []subagents.Task) (string, error) {
	projected := make([]fingerprintTask, len(tasks))
	for i, task := range tasks {
		projected[i] = fingerprintTask{
			ID: task.ID, Name: task.Name, DependsOn: task.DependsOn, Input: task.Input,
			Timeout: task.Timeout, Budget: task.Budget, Scope: task.Scope, Permission: task.Permission,
			AgentName: task.AgentName, AgentDigest: task.AgentDigest, Skill: task.Skill,
			ProviderName: task.ProviderName, Model: task.Model,
			OutputSchema: task.OutputSchema, InputSchema: task.InputSchema,
		}
	}
	payload, err := json.Marshal(projected)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

// idempotencyScope returns a fixed-length namespace for the caller principal.
// It matches cli.orchestrationPrincipal: SessionID and Role define ownership.
// Callers without a SessionID intentionally share the compatibility scope.
func idempotencyScope(ctx context.Context) string {
	caller, ok := runtime.CallerFrom(ctx)
	if !ok || caller.SessionID == "" {
		caller = runtime.Caller{}
	}
	payload, _ := json.Marshal(struct {
		Domain    string `json:"domain"`
		SessionID string `json:"session_id"`
		Role      string `json:"role"`
	}{"mivia:idempotency-scope:v1", caller.SessionID, caller.Role})
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest[:])
}

// scopedKey namespaces a non-empty idempotency key to its caller principal.
// Empty keys remain empty so unkeyed spawns never become idempotent.
func scopedKey(ctx context.Context, key string) string {
	if key == "" {
		return ""
	}
	caller, ok := runtime.CallerFrom(ctx)
	if !ok || caller.SessionID == "" {
		// Direct coordinator callers predate caller attribution and intentionally
		// share the legacy raw-key compatibility scope.
		return key
	}
	return idempotencyScope(ctx) + ":" + key
}

func (c *coordinator) lookupHandle(key string) *RunHandle {
	if key == "" {
		return nil
	}
	c.handlesMu.Lock()
	defer c.handlesMu.Unlock()
	return c.handles[key]
}

type namedTask struct {
	task                           subagents.Task
	taskID, displayName, attemptID string
}

func (c *coordinator) createTasks(ctx context.Context, runID string, tasks []subagents.Task, now time.Time) ([]namedTask, error) {
	out := make([]namedTask, 0, len(tasks))
	for _, task := range tasks {
		named, err := c.createTask(ctx, runID, task, now)
		if err != nil {
			return nil, err
		}
		out = append(out, named)
	}
	return out, nil
}

func (c *coordinator) createTask(ctx context.Context, runID string, task subagents.Task, now time.Time) (namedTask, error) {
	taskID := task.ID
	if taskID == "" {
		taskID = newTaskID()
	}
	displayName := c.names.Generate(task.Name)
	if displayName == "" {
		displayName = c.names.Generate("task")
	}
	attemptID := newAttemptID()
	snap := ledger.TaskSnapshot{RunID: runID, TaskID: taskID, ParentTaskID: parentTaskID(task.Owner), DisplayName: displayName, HandlerName: task.Name, AgentName: task.AgentName, AgentDigest: task.AgentDigest, Skill: task.Skill, ProviderName: task.ProviderName, Model: task.Model, Input: task.Input, Timeout: task.Timeout, Budget: task.Budget, Depth: task.Depth, Status: string(ledger.TaskStatusQueued), DependsOn: append([]string(nil), task.DependsOn...), CreatedAt: now, Version: 1, Attempts: []ledger.AttemptSnapshot{{AttemptID: attemptID, TaskID: taskID, RunID: runID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusQueued)}}}
	if err := c.repo.CreateTask(ctx, snap); err != nil {
		return namedTask{}, fmt.Errorf("create task %q: %w", taskID, err)
	}
	event := ledger.LifecycleEvent{ID: newEventID(), RunID: runID, Kind: "task_created", TaskID: taskID}
	if err := c.repo.AppendEvent(ctx, event); err != nil {
		return namedTask{}, fmt.Errorf("create task event %q: %w", taskID, err)
	}
	c.emitLifecycleEvent(event)
	return namedTask{task: task, taskID: taskID, displayName: displayName, attemptID: attemptID}, nil
}

func (c *coordinator) newRunHandle(runID, key string, attempts map[string]string, fingerprint string, recovered bool) *RunHandle {
	poolCtx, cancel := context.WithCancel(context.Background())
	h := &RunHandle{
		runID: runID, done: make(chan struct{}), cancel: cancel, poolCtx: poolCtx,
		attempts: attempts, requestFingerprint: fingerprint, recovered: recovered,
		cancelDone: make(chan struct{}), owner: c,
		mailboxes: newRunMailboxes(32),
	}
	if key != "" {
		c.handlesMu.Lock()
		c.handles[key] = h
		c.handlesMu.Unlock()
		go c.evictHandleAfterTerminal(key, h)
	}
	return h
}

// releaseAndDeleteRun releases the execution claim on a run and deletes the
// run record. Used during error cleanup in createAndStartRun when the claim
// has already been acquired but a subsequent step fails.
func (c *coordinator) releaseAndDeleteRun(ctx context.Context, runID string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.repo.ReleaseRun(cleanupCtx, runID, c.holderID)
	_ = c.repo.DeleteRun(cleanupCtx, runID)
}
