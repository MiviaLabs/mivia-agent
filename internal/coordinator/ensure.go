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
		h, createErr := c.createAndStartRunWithID(ctx, req.RunID, req.Tasks, key, fingerprint, false, nonInteractiveRunOpts(req)...)
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
	go c.executeRun(h, tasks)
	return h, nil
}

// nonInteractiveRunOpts converts the ensure request's parent-interactivity flag
// into run-handle construction options (nil when the parent is interactive).
func nonInteractiveRunOpts(req EnsureRunRequest) []runHandleOption {
	if req.NonInteractiveParent {
		return []runHandleOption{withNonInteractiveParent()}
	}
	return nil
}

// claimForResume acquires the execution claim for a (force-)resume without
// ever wiping a LIVE claim blindly. ClaimRun is probed first; only a claim
// that is actually held is cleared, and only once - a second ErrClaimHeld
// means another executor holds the run right now, so force-resume refuses
// too. The probe claim (same holder) is kept where the caller executes the
// run itself; the interrupted-run path's own ClaimRun is a same-holder
// refresh, so the run stays exclusively ours across the resume handoff.
func (c *coordinator) claimForResume(ctx context.Context, runID string, force bool) error {
	if err := c.repo.ClaimRun(ctx, runID, c.holderID); err != nil {
		if errors.Is(err, ledger.ErrClaimHeld) && force {
			if err := c.repo.ClearRunClaim(ctx, runID); err != nil {
				return fmt.Errorf("ensure run: clear run claim: %w", err)
			}
			if err := c.repo.ClaimRun(ctx, runID, c.holderID); err != nil {
				if errors.Is(err, ledger.ErrClaimHeld) {
					return ErrRunHeldByAnotherExecutor
				}
				return fmt.Errorf("ensure run: reclaim run: %w", err)
			}
		} else if errors.Is(err, ledger.ErrClaimHeld) {
			return ErrRunHeldByAnotherExecutor
		} else {
			return fmt.Errorf("ensure run: claim run: %w", err)
		}
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
