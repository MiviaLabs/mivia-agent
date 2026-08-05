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
}

// EnsureRun creates or resumes the exact host-admitted run.
func (c *coordinator) EnsureRun(ctx context.Context, req EnsureRunRequest) (*RunHandle, error) {
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
		h, createErr := c.createAndStartRunWithID(ctx, req.RunID, req.Tasks, key, fingerprint, false)
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
		h := c.newRunHandle(snap.RunID, key, latestAttempts(storedTasks), fingerprint, true)
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
		if err := c.repo.ClearRunClaim(ctx, snap.RunID); err != nil {
			return nil, fmt.Errorf("ensure run: clear run claim: %w", err)
		}
	}
	h, err := c.resumeInterruptedRun(ctx, snap.RunID, req.Tasks)
	if err != nil {
		return nil, err
	}
	h.requestFingerprint = fingerprint
	c.registerEnsuredHandle(key, h)
	return h, nil
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
	fingerprint, err := requestFingerprint(req.Tasks)
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
	}})
	right, rightErr := requestFingerprint([]subagents.Task{{
		ID: snap.TaskID, Name: snap.HandlerName, DependsOn: snap.DependsOn, Input: snap.Input,
		Timeout: snap.Timeout, Budget: snap.Budget, Scope: snap.Scope,
		AgentName: snap.AgentName, AgentDigest: snap.AgentDigest, Skill: snap.Skill,
		ProviderName: snap.ProviderName, Model: snap.Model, OutputSchema: snap.OutputSchema,
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
	if req.ForceResume {
		if err := c.repo.ClearRunClaim(ctx, runID); err != nil {
			return nil, fmt.Errorf("ensure run: clear run claim: %w", err)
		}
	}
	if err := c.repo.ClaimRun(ctx, runID, c.holderID); err != nil {
		if errors.Is(err, ledger.ErrClaimHeld) {
			return nil, ErrRunHeldByAnotherExecutor
		}
		return nil, fmt.Errorf("ensure run: claim empty run: %w", err)
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
	h := c.newRunHandle(runID, key, attempts, fingerprint, false)
	go c.executeRun(h, tasks)
	return h, nil
}

func (c *coordinator) registerEnsuredHandle(key string, h *RunHandle) {
	c.handlesMu.Lock()
	c.handles[key] = h
	c.handlesMu.Unlock()
	go c.evictHandleAfterTerminal(key, h)
}
