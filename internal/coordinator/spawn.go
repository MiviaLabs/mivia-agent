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
	policy := policyWithRetry(ledger.RunPolicy{}, c.retryPolicyLocked())
	fingerprint, err := requestFingerprintWithPolicy(tasks, policy)
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
	return c.createAndStartRun(ctx, tasks, key, fingerprint, policy)
}

func (c *coordinator) createAndStartRun(ctx context.Context, tasks []subagents.Task, key, fingerprint string, policy ledger.RunPolicy) (*RunHandle, error) {
	return c.createAndStartRunWithID(ctx, newRunID(), tasks, key, fingerprint, policy, true)
}

func (c *coordinator) createAndStartRunWithID(ctx context.Context, runID string, tasks []subagents.Task, key, fingerprint string, policy ledger.RunPolicy, recoverDuplicate bool, opts ...runHandleOption) (*RunHandle, error) {
	now := c.nowLocked()
	run := ledger.RunSnapshot{RunID: runID, DisplayName: c.names.Generate("run"), Status: ledger.RunStatusCreated, RequestFingerprint: fingerprint, CreatedAt: now, Labels: map[string]string{}, Tasks: make([]ledger.TaskSnapshot, 0, len(tasks)), Policy: policy}
	if err := c.repo.CreateRun(ctx, key, run); err != nil {
		if errors.Is(err, ledger.ErrDuplicate) && key != "" && recoverDuplicate {
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
			return nil, ErrRunHeldByAnotherExecutor
		}
		return nil, fmt.Errorf("claim run %q: %w", runID, err)
	}

	// run_created has no single task in hand, so SessionID is deliberately left
	// empty: run-level events are correlated via RunID, not caller session.
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
	opts = append(opts, withRunPolicy(policy))
	h := c.newRunHandle(runID, key, attempts, fingerprint, false, opts...)
	h.mu.Lock()
	h.localActor = true
	h.mu.Unlock()
	// Stamp pool context before starting the run goroutine so concurrent
	// referral spawns never race the first poolCtx write (plan 53.04).
	h.mu.Lock()
	h.poolCtx = contextWithRunExec(h.poolCtx, runID, ledgerTasks, h.mailboxes)
	h.mu.Unlock()
	go c.executeRun(h, ledgerTasks)
	return h, nil
}

var ErrIdempotencyConflict = errors.New("idempotency key already used for a different request")

// fingerprintTask is the explicit list of fields that describe the requested
// work. It is a projection of subagents.Task, not the struct itself: caller
// identity is deliberately excluded because it is idempotency-key scope, not
// requested work. Add new work-defining Task fields here deliberately.
type fingerprintTask struct {
	ID                    string             `json:"id"`
	Name                  string             `json:"name"`
	DependsOn             []string           `json:"depends_on,omitempty"`
	Input                 json.RawMessage    `json:"input,omitempty"`
	Timeout               time.Duration      `json:"timeout,omitempty"`
	Budget                int                `json:"budget,omitempty"`
	Scope                 string             `json:"scope,omitempty"`
	AgentName             string             `json:"agent_name"`
	AgentDigest           string             `json:"agent_digest"`
	Skill                 string             `json:"skill,omitempty"`
	ProviderName          string             `json:"provider_name,omitempty"`
	Model                 string             `json:"model,omitempty"`
	OutputSchema          map[string]any     `json:"output_schema,omitempty"`
	InputSchema           map[string]any     `json:"input_schema,omitempty"`
	WorkLimits            runtime.WorkLimits `json:"work_limits,omitempty"`
	DisableProviderReplay bool               `json:"disable_provider_replay,omitempty"`
}

// requestFingerprint returns the canonical identity of the work in tasks.
func requestFingerprint(tasks []subagents.Task) (string, error) {
	projected := make([]fingerprintTask, len(tasks))
	for i, task := range tasks {
		projected[i] = fingerprintTask{
			ID: task.ID, Name: task.Name, DependsOn: task.DependsOn, Input: task.Input,
			Timeout: task.Timeout, Budget: task.Budget, Scope: task.Scope,
			AgentName: task.AgentName, AgentDigest: task.AgentDigest, Skill: task.Skill,
			ProviderName: task.ProviderName, Model: task.Model,
			OutputSchema: task.OutputSchema, InputSchema: task.InputSchema,
			WorkLimits: task.WorkLimits, DisableProviderReplay: task.DisableProviderReplay,
		}
	}
	payload, err := json.Marshal(projected)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func requestFingerprintWithPolicy(tasks []subagents.Task, policy ledger.RunPolicy) (string, error) {
	base, err := requestFingerprint(tasks)
	if err != nil {
		return "", err
	}
	if policy == (ledger.RunPolicy{}) {
		return base, nil
	}
	payload, err := json.Marshal(struct {
		WorkFingerprint string           `json:"work_fingerprint"`
		Policy          ledger.RunPolicy `json:"policy"`
	}{WorkFingerprint: base, Policy: policy})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

// RequestFingerprint returns the canonical coordinator work fingerprint.
// Callers use it to verify a persisted, non-authority work specification.
func RequestFingerprint(tasks []subagents.Task, policy ledger.RunPolicy) (string, error) {
	return requestFingerprintWithPolicy(tasks, policy)
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
	snap := ledger.TaskSnapshot{RunID: runID, TaskID: taskID, ParentTaskID: parentTaskID(task.Owner), DisplayName: displayName, HandlerName: task.Name, AgentName: task.AgentName, AgentDigest: task.AgentDigest, Skill: task.Skill, ProviderName: task.ProviderName, Model: task.Model, Scope: task.Scope, OutputSchema: task.OutputSchema, InputSchema: task.InputSchema, Input: task.Input, Timeout: task.Timeout, Budget: task.Budget, Depth: task.Depth, WorkLimits: task.WorkLimits, DisableProviderReplay: task.DisableProviderReplay, Status: string(ledger.TaskStatusQueued), DependsOn: append([]string(nil), task.DependsOn...), CreatedAt: now, Version: 1, Attempts: []ledger.AttemptSnapshot{{AttemptID: attemptID, TaskID: taskID, RunID: runID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusQueued)}}}
	if err := c.repo.CreateTask(ctx, snap); err != nil {
		return namedTask{}, fmt.Errorf("create task %q: %w", taskID, err)
	}
	event := ledger.LifecycleEvent{ID: newEventID(), RunID: runID, Kind: "task_created", TaskID: taskID, SessionID: task.SessionID}
	if err := c.repo.AppendEvent(ctx, event); err != nil {
		return namedTask{}, fmt.Errorf("create task event %q: %w", taskID, err)
	}
	c.emitLifecycleEvent(event)
	return namedTask{task: task, taskID: taskID, displayName: displayName, attemptID: attemptID}, nil
}

// runHandleOption mutates a RunHandle at construction time. Options apply
// before the handle is registered, so they are visible to every pool worker
// the moment the run starts.
type runHandleOption func(*RunHandle)

func withRunPolicy(policy ledger.RunPolicy) runHandleOption {
	return func(h *RunHandle) {
		h.retryPolicy = retryPolicyFromRunPolicy(policy)
		h.failInterrupted = policy.FailInterrupted
	}
}

// withNonInteractiveParent marks a run whose parent is a non-interactive
// controller that can never answer child questions. ParkQuestion then declines
// the run's child questions immediately at park time (generic mechanism; the
// coordinator never assumes who the parent is).
func withNonInteractiveParent() runHandleOption {
	return func(h *RunHandle) { h.nonInteractiveParent = true }
}

func (c *coordinator) newRunHandle(runID, key string, attempts map[string]string, fingerprint string, recovered bool, opts ...runHandleOption) *RunHandle {
	poolCtx, cancel := context.WithCancel(context.Background())
	h := &RunHandle{
		runID: runID, done: make(chan struct{}), cancel: cancel, poolCtx: poolCtx,
		attempts: attempts, requestFingerprint: fingerprint, recovered: recovered,
		retryPolicy: c.retryPolicyLocked(),
		cancelDone:  make(chan struct{}), owner: c,
		mailboxes: newRunMailboxes(c.mailboxCapacity),
	}
	for _, opt := range opts {
		opt(h)
	}
	c.handlesMu.Lock()
	c.handlesByRun[runID] = h
	if key != "" {
		c.handles[key] = h
	}
	c.handlesMu.Unlock()
	if key != "" {
		go c.evictHandleAfterTerminal(key, h)
	}
	return h
}

// releaseAndDeleteRun deletes a run while this coordinator still holds its
// execution claim. It releases the claim after the delete attempt. This order
// prevents another host from claiming the run before cleanup deletes it.
func (c *coordinator) releaseAndDeleteRun(ctx context.Context, runID string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.repo.DeleteRun(cleanupCtx, runID)
	_ = c.repo.ReleaseRun(cleanupCtx, runID, c.holderID)
}
