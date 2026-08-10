package coordinator

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// EnsureRunRequest identifies one host-admitted run and its exact work.
type EnsureRunRequest struct {
	RunID          string
	Tasks          []subagents.Task
	IdempotencyKey string
	ForceResume    bool
	// NonInteractiveParent marks the run's parent as a non-interactive
	// controller that can never answer child questions. Parked questions for
	// tasks in such a run are declined immediately at park time so the child
	// proceeds instead of burning its full wait budget. Generic mechanism: the
	// coordinator never assumes who the parent is, only that it cannot answer.
	NonInteractiveParent bool
	Policy               ledger.RunPolicy
}

// EnsureRun creates or resumes the exact host-admitted run.
func (c *coordinator) EnsureRun(ctx context.Context, req EnsureRunRequest) (*RunHandle, error) {
	var err error
	req, err = c.resolveEnsurePolicy(ctx, req)
	if err != nil {
		return nil, err
	}
	fingerprint, key, err := c.validateEnsureRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	c.spawnMu.Lock()
	defer c.spawnMu.Unlock()
	if h := c.lookupHandle(key); h != nil {
		if h.runID != req.RunID || h.requestFingerprint != fingerprint {
			return nil, ErrIdempotencyConflict
		}
		return h, nil
	}

	snap, err := c.repo.GetRunByIdempotencyKey(ctx, key)
	if errors.Is(err, ledger.ErrNotFound) {
		h, createErr := c.createAndStartRunWithID(ctx, req.RunID, req.Tasks, key, fingerprint, req.Policy, false, nonInteractiveRunOpts(req)...)
		if createErr == nil {
			return h, nil
		}
		if !errors.Is(createErr, ledger.ErrDuplicate) {
			return nil, createErr
		}
		// Another host can win creation after the first lookup. Read its
		// durable tuple and continue through the same exact validation path.
		snap, err = c.repo.GetRunByIdempotencyKey(ctx, key)
	}
	if err != nil {
		return nil, fmt.Errorf("ensure run: get idempotent run: %w", err)
	}
	if snap.RunID != req.RunID || snap.RequestFingerprint != fingerprint {
		return nil, ErrIdempotencyConflict
	}
	storedTasks, err := c.repo.ListTasks(ctx, snap.RunID)
	if err != nil {
		return nil, fmt.Errorf("ensure run: list tasks: %w", err)
	}
	if len(storedTasks) != 0 && len(storedTasks) != len(req.Tasks) {
		return nil, fmt.Errorf("ensure run: partial task admission for run %q", snap.RunID)
	}
	if len(storedTasks) > 0 {
		if err := validateStoredAdmission(req.Tasks, storedTasks); err != nil {
			return nil, err
		}
	}
	if isTerminalRunStatus(snap.Status) {
		h := c.newRunHandle(snap.RunID, key, latestAttempts(storedTasks), fingerprint, true, nonInteractiveRunOpts(req)...)
		go c.watchRecoveredRun(h)
		return h, nil
	}
	if h := c.HandleForRun(snap.RunID); h != nil {
		return h, nil
	}
	if len(storedTasks) == 0 {
		return c.resumeEmptyRun(ctx, snap.RunID, key, fingerprint, req)
	}
	if req.ForceResume {
		if err := c.claimForResume(ctx, snap.RunID, true); err != nil {
			return nil, err
		}
	}
	h, err := c.resumeInterruptedRun(ctx, snap.RunID, req.Tasks, nonInteractiveRunOpts(req)...)
	if err != nil {
		return nil, err
	}
	h.requestFingerprint = fingerprint
	c.registerEnsuredHandle(key, h)
	return h, nil
}

// EnsureSingleTaskRun admits exactly one runnable task. Wave 3 callers use
// this operation instead of general DAG admission.
func (c *coordinator) EnsureSingleTaskRun(ctx context.Context, req EnsureRunRequest) (*RunHandle, error) {
	if len(req.Tasks) != 1 {
		return nil, fmt.Errorf("ensure single task run: want one task")
	}
	var err error
	req, err = c.resolveEnsurePolicy(ctx, req)
	if err != nil {
		return nil, err
	}
	fingerprint, key, err := c.validateEnsureRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	c.spawnMu.Lock()
	defer c.spawnMu.Unlock()
	if h := c.lookupHandle(key); h != nil {
		if h.runID != req.RunID || h.requestFingerprint != fingerprint {
			return nil, ErrIdempotencyConflict
		}
		return h, nil
	}
	if snap, err := c.repo.GetRunByIdempotencyKey(ctx, key); err == nil {
		_ = snap
		return c.joinSingleTaskAdmission(ctx, req, key, fingerprint)
	} else if !errors.Is(err, ledger.ErrNotFound) {
		return nil, err
	}
	if req.ForceResume {
		return nil, ledger.ErrNotFound
	}
	now := c.nowLocked()
	task := req.Tasks[0]
	attemptID := newAttemptID()
	taskSnap := ledger.TaskSnapshot{RunID: req.RunID, TaskID: task.ID, HandlerName: task.Name, AgentName: task.AgentName, AgentDigest: task.AgentDigest, Skill: task.Skill, ProviderName: task.ProviderName, Model: task.Model, Scope: task.Scope, Input: task.Input, InputSchema: task.InputSchema, OutputSchema: task.OutputSchema, Timeout: task.Timeout, Budget: task.Budget, Depth: task.Depth, WorkLimits: task.WorkLimits, DisableProviderReplay: task.DisableProviderReplay, DependsOn: append([]string(nil), task.DependsOn...), Status: string(ledger.TaskStatusQueued), Version: 1, CreatedAt: now, Attempts: []ledger.AttemptSnapshot{{AttemptID: attemptID, TaskID: task.ID, RunID: req.RunID, AttemptNum: 1, StartedAt: now, Status: string(ledger.TaskStatusQueued)}}}
	run := ledger.RunSnapshot{RunID: req.RunID, DisplayName: c.names.Generate("run"), Status: ledger.RunStatusCreated, RequestFingerprint: fingerprint, CreatedAt: now, Policy: req.Policy}
	if err := c.repo.AdmitSingleTask(ctx, ledger.SingleTaskAdmission{IdempotencyKey: key, Run: run, Task: taskSnap}); err != nil {
		if errors.Is(err, ledger.ErrDuplicate) {
			return c.joinSingleTaskAdmission(ctx, req, key, fingerprint)
		}
		return nil, err
	}
	if err := c.repo.ClaimRun(ctx, req.RunID, c.holderID); err != nil {
		return nil, err
	}
	opts := append(nonInteractiveRunOpts(req), withRunPolicy(req.Policy))
	h := c.newRunHandle(req.RunID, key, map[string]string{task.ID: attemptID}, fingerprint, false, opts...)
	h.mu.Lock()
	h.localActor = true
	h.mu.Unlock()
	go c.executeRun(h, []subagents.Task{task})
	return h, nil
}

// EnsureTerminalSingleTaskRun admits a canceled single-task tombstone.
func (c *coordinator) EnsureTerminalSingleTaskRun(ctx context.Context, req EnsureRunRequest, status ledger.TaskStatus) (*RunHandle, error) {
	if status != ledger.TaskStatusCanceled {
		return nil, fmt.Errorf("ensure terminal single task run: unsupported status %q", status)
	}
	if len(req.Tasks) != 1 {
		return nil, fmt.Errorf("ensure terminal single task run: want one task")
	}
	var err error
	req, err = c.resolveEnsurePolicy(ctx, req)
	if err != nil {
		return nil, err
	}
	fingerprint, key, err := c.validateEnsureRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	c.spawnMu.Lock()
	defer c.spawnMu.Unlock()
	if h := c.lookupHandle(key); h != nil {
		if h.runID != req.RunID || h.requestFingerprint != fingerprint {
			return nil, ErrIdempotencyConflict
		}
		return h, nil
	}
	if snap, err := c.repo.GetRunByIdempotencyKey(ctx, key); err == nil {
		_ = snap
		return c.joinSingleTaskAdmission(ctx, req, key, fingerprint)
	} else if !errors.Is(err, ledger.ErrNotFound) {
		return nil, err
	}
	now := c.nowLocked()
	run := ledger.RunSnapshot{RunID: req.RunID, DisplayName: c.names.Generate("run"), Status: ledger.RunStatusCanceled, RequestFingerprint: fingerprint, CreatedAt: now, CompletedAt: &now, Policy: req.Policy}
	task := req.Tasks[0]
	snap := ledger.TaskSnapshot{RunID: req.RunID, TaskID: task.ID, HandlerName: task.Name, AgentName: task.AgentName, AgentDigest: task.AgentDigest, Skill: task.Skill, ProviderName: task.ProviderName, Model: task.Model, Scope: task.Scope, Input: task.Input, InputSchema: task.InputSchema, OutputSchema: task.OutputSchema, Timeout: task.Timeout, Budget: task.Budget, WorkLimits: task.WorkLimits, DisableProviderReplay: task.DisableProviderReplay, DependsOn: append([]string(nil), task.DependsOn...), Status: string(status), Version: 1, CreatedAt: now, CompletedAt: &now, Attempts: []ledger.AttemptSnapshot{{AttemptID: newAttemptID(), TaskID: task.ID, RunID: req.RunID, AttemptNum: 1, StartedAt: now, FinishedAt: &now, Status: string(status)}}}
	if err := c.repo.AdmitSingleTask(ctx, ledger.SingleTaskAdmission{IdempotencyKey: key, Run: run, Task: snap}); err != nil {
		if errors.Is(err, ledger.ErrDuplicate) {
			return c.joinSingleTaskAdmission(ctx, req, key, fingerprint)
		}
		return nil, err
	}
	h := c.newRunHandle(req.RunID, key, latestAttempts([]ledger.TaskSnapshot{snap}), fingerprint, true, nonInteractiveRunOpts(req)...)
	go c.watchRecoveredRun(h)
	return h, nil
}

// NeedsActorPermit reports whether an admitted child needs a local actor
// permit. It never admits work. It briefly claims a durable child to tell an
// unclaimed resumable child from a remote wait-only child.
func (c *coordinator) NeedsActorPermit(ctx context.Context, req EnsureRunRequest) (bool, error) {
	var err error
	req, err = c.resolveEnsurePolicy(ctx, req)
	if err != nil {
		return false, err
	}
	fingerprint, key, err := c.validateEnsureRequest(ctx, req)
	if err != nil {
		return false, err
	}
	if h := c.lookupHandle(key); h != nil {
		return h.LocalActor(), nil
	}
	run, err := c.repo.GetRunByIdempotencyKey(ctx, key)
	if errors.Is(err, ledger.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if run.RunID != req.RunID || run.RequestFingerprint != fingerprint || run.Policy != req.Policy {
		return false, ErrIdempotencyConflict
	}
	if isTerminalRunStatus(run.Status) {
		return false, nil
	}
	if err := c.repo.ClaimRun(ctx, run.RunID, c.holderID); err == nil {
		_ = c.repo.ReleaseRun(context.Background(), run.RunID, c.holderID)
		return true, nil
	} else if errors.Is(err, ledger.ErrClaimHeld) {
		return false, nil
	} else {
		return false, err
	}
}

// resolveEnsurePolicy restores the durable policy for an omitted request.
// An explicit non-zero policy is later compared with the durable tuple.
func (c *coordinator) resolveEnsurePolicy(ctx context.Context, req EnsureRunRequest) (EnsureRunRequest, error) {
	if req.Policy != (ledger.RunPolicy{}) || strings.TrimSpace(req.IdempotencyKey) == "" {
		req.Policy = policyWithRetry(req.Policy, c.retryPolicyLocked())
		return req, nil
	}
	snap, err := c.repo.GetRunByIdempotencyKey(ctx, scopedKey(ctx, req.IdempotencyKey))
	if errors.Is(err, ledger.ErrNotFound) {
		req.Policy = policyWithRetry(req.Policy, c.retryPolicyLocked())
		return req, nil
	}
	if err != nil {
		return req, err
	}
	req.Policy = snap.Policy
	return req, nil
}

// joinSingleTaskAdmission validates and joins a durable admission winner.
// It never changes the winner's terminal or runnable state.
func (c *coordinator) joinSingleTaskAdmission(ctx context.Context, req EnsureRunRequest, key, fingerprint string) (*RunHandle, error) {
	run, err := c.repo.GetRunByIdempotencyKey(ctx, key)
	if err != nil {
		if errors.Is(err, ledger.ErrNotFound) {
			return nil, ErrIdempotencyConflict
		}
		return nil, fmt.Errorf("join single admission: lookup winner: %w", err)
	}
	if run.RunID != req.RunID || run.RequestFingerprint != fingerprint || run.Policy != req.Policy {
		return nil, ErrIdempotencyConflict
	}
	tasks, err := c.repo.ListTasks(ctx, run.RunID)
	if err != nil {
		return nil, fmt.Errorf("join single admission: list winner tasks: %w", err)
	}
	if len(tasks) != 1 || !sameStoredWork(req.Tasks[0], tasks[0]) {
		return nil, ErrIdempotencyConflict
	}
	if isTerminalRunStatus(run.Status) {
		h := c.newRunHandle(run.RunID, key, latestAttempts(tasks), fingerprint, true, nonInteractiveRunOpts(req)...)
		go c.watchRecoveredRun(h)
		return h, nil
	}
	if h := c.HandleForRun(run.RunID); h != nil {
		return h, nil
	}
	if panelWaitOnlyJoin(ctx) {
		if err := c.repo.ClaimRun(ctx, run.RunID, c.holderID); err == nil {
			_ = c.repo.ReleaseRun(context.Background(), run.RunID, c.holderID)
			return nil, ErrWaitOnlyJoinLost
		} else if errors.Is(err, ledger.ErrClaimHeld) {
			h := c.newRunHandle(run.RunID, key, latestAttempts(tasks), fingerprint, true, nonInteractiveRunOpts(req)...)
			go c.watchJoinedRun(h)
			return h, nil
		} else {
			return nil, err
		}
	}
	if err := c.claimRun(ctx, run.RunID); errors.Is(err, ledger.ErrClaimHeld) {
		h := c.newRunHandle(run.RunID, key, latestAttempts(tasks), fingerprint, true, nonInteractiveRunOpts(req)...)
		go c.watchJoinedRun(h)
		return h, nil
	} else if err != nil {
		return nil, err
	}
	// Keep this claim through the resume handoff. Releasing it here lets a
	// winner complete or another executor claim the run between the probe and
	// resume. resumeInterruptedRun refreshes this same holder claim.
	current, err := c.repo.GetRun(ctx, run.RunID)
	if err != nil {
		_ = c.repo.ReleaseRun(ctx, run.RunID, c.holderID)
		return nil, err
	}
	if isTerminalRunStatus(current.Status) {
		_ = c.repo.ReleaseRun(ctx, run.RunID, c.holderID)
		h := c.newRunHandle(run.RunID, key, latestAttempts(tasks), fingerprint, true, nonInteractiveRunOpts(req)...)
		go c.watchRecoveredRun(h)
		return h, nil
	}
	return c.resumeInterruptedRun(ctx, run.RunID, req.Tasks, nonInteractiveRunOpts(req)...)
}

func (c *coordinator) validateEnsureRequest(ctx context.Context, req EnsureRunRequest) (string, string, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return "", "", fmt.Errorf("ensure run: idempotency key is empty")
	}
	if !validRunID(req.RunID) {
		return "", "", fmt.Errorf("ensure run: run ID is invalid")
	}
	if err := c.validateTasks(req.Tasks); err != nil {
		return "", "", fmt.Errorf("ensure run: %w", err)
	}
	for _, task := range req.Tasks {
		if task.ID == "" {
			return "", "", fmt.Errorf("ensure run: task ID is empty")
		}
	}
	fingerprint, err := requestFingerprintWithPolicy(req.Tasks, req.Policy)
	if err != nil {
		return "", "", fmt.Errorf("ensure run: fingerprint request: %w", err)
	}
	return fingerprint, scopedKey(ctx, req.IdempotencyKey), nil
}

func validateStoredAdmission(requested []subagents.Task, stored []ledger.TaskSnapshot) error {
	byID := make(map[string]ledger.TaskSnapshot, len(stored))
	for _, task := range stored {
		byID[task.TaskID] = task
	}
	for _, task := range requested {
		snap, ok := byID[task.ID]
		if !ok || !sameStoredWork(task, snap) {
			return fmt.Errorf("ensure run: stored task %q does not match the request", task.ID)
		}
	}
	return nil
}

func sameStoredWork(task subagents.Task, snap ledger.TaskSnapshot) bool {
	left, leftErr := requestFingerprint([]subagents.Task{{
		ID: task.ID, Name: task.Name, DependsOn: task.DependsOn, Input: task.Input,
		Timeout: task.Timeout, Budget: task.Budget, Scope: task.Scope,
		AgentName: task.AgentName, AgentDigest: task.AgentDigest, Skill: task.Skill,
		ProviderName: task.ProviderName, Model: task.Model, OutputSchema: task.OutputSchema,
		InputSchema: task.InputSchema, WorkLimits: task.WorkLimits, DisableProviderReplay: task.DisableProviderReplay,
	}})
	right, rightErr := requestFingerprint([]subagents.Task{{
		ID: snap.TaskID, Name: snap.HandlerName, DependsOn: snap.DependsOn, Input: snap.Input,
		Timeout: snap.Timeout, Budget: snap.Budget, Scope: snap.Scope,
		AgentName: snap.AgentName, AgentDigest: snap.AgentDigest, Skill: snap.Skill,
		ProviderName: snap.ProviderName, Model: snap.Model, OutputSchema: snap.OutputSchema,
		InputSchema: snap.InputSchema, WorkLimits: snap.WorkLimits, DisableProviderReplay: snap.DisableProviderReplay,
	}})
	return leftErr == nil && rightErr == nil && left == right
}

func validRunID(runID string) bool {
	const prefix = "run-"
	if !strings.HasPrefix(runID, prefix) {
		return false
	}
	token := strings.TrimPrefix(runID, prefix)
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(token)
	canonical := prefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(decoded)
	return err == nil && len(decoded) == 16 && canonical == runID
}

func latestAttempts(tasks []ledger.TaskSnapshot) map[string]string {
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
	return attempts
}

func (c *coordinator) resumeEmptyRun(ctx context.Context, runID, key, fingerprint string, req EnsureRunRequest) (*RunHandle, error) {
	// Claim first; only a held claim is ever cleared, and only once, so a
	// force resume cannot wipe a live claim held by another executor.
	if err := c.claimForResume(ctx, runID, req.ForceResume); err != nil {
		return nil, err
	}
	named, err := c.createTasks(ctx, runID, req.Tasks, c.nowLocked())
	if err != nil {
		_ = c.repo.ReleaseRun(ctx, runID, c.holderID)
		return nil, fmt.Errorf("ensure run: repair task admission: %w", err)
	}
	attempts := make(map[string]string, len(named))
	tasks := make([]subagents.Task, len(named))
	for i, task := range named {
		attempts[task.taskID] = task.attemptID
		tasks[i] = task.task
		tasks[i].ID = task.taskID
	}
	h := c.newRunHandle(runID, key, attempts, fingerprint, false, nonInteractiveRunOpts(req)...)
	h.mu.Lock()
	h.localActor = true
	h.mu.Unlock()
	go c.executeRun(h, tasks)
	return h, nil
}

// nonInteractiveRunOpts converts the ensure request's parent-interactivity flag
// into run-handle construction options (nil when the parent is interactive).
func nonInteractiveRunOpts(req EnsureRunRequest) []runHandleOption {
	opts := []runHandleOption{withRunPolicy(req.Policy)}
	if req.NonInteractiveParent {
		opts = append(opts, withNonInteractiveParent())
	}
	return opts
}

// claimForResume acquires a free or expired execution claim. Force resume does
// not clear a live claim. The interrupted-run path refreshes the same lease.
func (c *coordinator) claimForResume(ctx context.Context, runID string, force bool) error {
	_ = force
	if err := c.claimRun(ctx, runID); err != nil {
		if errors.Is(err, ledger.ErrClaimHeld) {
			return ErrRunHeldByAnotherExecutor
		}
		return fmt.Errorf("ensure run: claim run: %w", err)
	}
	return nil
}

// registerEnsuredHandle stores the run handle under its idempotency key so a
// repeat EnsureRun for the same key returns the same handle, then evicts it
// once the run reaches a terminal state.
func (c *coordinator) registerEnsuredHandle(key string, h *RunHandle) {
	c.handlesMu.Lock()
	c.handles[key] = h
	c.handlesMu.Unlock()
	go c.evictHandleAfterTerminal(key, h)
}
