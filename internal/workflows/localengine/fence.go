package localengine

import (
	"context"
	"sync"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// abandonFence wraps a Repository so abandoned runs cannot be mutated by a
// dying controller goroutine after Interrupt. Reads pass through; terminal
// status CAS and attempt completion for abandoned runs fail closed.
type abandonFence struct {
	inner     workflowledger.Repository
	mu        sync.Mutex
	abandoned map[string]struct{}
}

func newAbandonFence(inner workflowledger.Repository) *abandonFence {
	return &abandonFence{inner: inner, abandoned: make(map[string]struct{})}
}

func (f *abandonFence) abandon(runID string) {
	f.mu.Lock()
	f.abandoned[runID] = struct{}{}
	f.mu.Unlock()
}

// clearAbandon allows a resumed controller to write again after Interrupt.
// Callers must use this method; never mutate abandoned without f.mu.
func (f *abandonFence) clearAbandon(runID string) {
	f.mu.Lock()
	delete(f.abandoned, runID)
	f.mu.Unlock()
}

func (f *abandonFence) isAbandoned(runID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.abandoned[runID]
	return ok
}

// mutate serializes a run-bound write with abandon. A write that starts first
// completes before abandon returns. A write that starts after abandon fails.
func (f *abandonFence) mutate(runID string, write func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, abandoned := f.abandoned[runID]; abandoned {
		return workflowledger.ErrConflict
	}
	return write()
}

func (f *abandonFence) CreateRun(ctx context.Context, snap workflowledger.RunSnapshot, snapshotJSON []byte) error {
	return f.mutate(snap.RunID, func() error { return f.inner.CreateRun(ctx, snap, snapshotJSON) })
}

func (f *abandonFence) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	return f.inner.GetRun(ctx, runID)
}

func (f *abandonFence) ListRuns(ctx context.Context, status ...workflowledger.RunStatus) ([]workflowledger.RunSnapshot, error) {
	return f.inner.ListRuns(ctx, status...)
}

func (f *abandonFence) GetRunSnapshot(ctx context.Context, runID string) ([]byte, error) {
	return f.inner.GetRunSnapshot(ctx, runID)
}

func (f *abandonFence) CompareAndSetRunStatus(ctx context.Context, runID string, expectedVersion uint64, status workflowledger.RunStatus, finishedAt *time.Time) error {
	return f.mutate(runID, func() error { return f.inner.CompareAndSetRunStatus(ctx, runID, expectedVersion, status, finishedAt) })
}

func (f *abandonFence) DeleteRun(ctx context.Context, runID string) error {
	return f.mutate(runID, func() error { return f.inner.DeleteRun(ctx, runID) })
}

func (f *abandonFence) RecordRunResumed(ctx context.Context, runID string) error {
	return f.mutate(runID, func() error { return f.inner.RecordRunResumed(ctx, runID) })
}

func (f *abandonFence) CreateStepAttempt(ctx context.Context, attempt workflowledger.StepAttempt) error {
	return f.mutate(attempt.RunID, func() error { return f.inner.CreateStepAttempt(ctx, attempt) })
}

func (f *abandonFence) GetStepAttempt(ctx context.Context, runID, attemptID string) (workflowledger.StepAttempt, error) {
	return f.inner.GetStepAttempt(ctx, runID, attemptID)
}

func (f *abandonFence) ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error) {
	return f.inner.ListStepAttempts(ctx, runID)
}

func (f *abandonFence) CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error {
	return f.mutate(runID, func() error { return f.inner.CompleteStepAttempt(ctx, runID, attemptID, expectedVersion, outcome) })
}

func (f *abandonFence) RecordStepAttemptOutcome(ctx context.Context, attempt workflowledger.StepAttempt, outcome workflowledger.AttemptOutcome) error {
	return f.mutate(attempt.RunID, func() error { return f.inner.RecordStepAttemptOutcome(ctx, attempt, outcome) })
}

func (f *abandonFence) CompareAndSetPanelPhase(ctx context.Context, runID, attemptID string, expectedVersion uint64, from workflowledger.PanelPhase, to workflowledger.PanelPhase, synthesis *workflowledger.PanelSynthesisExecution) error {
	return f.mutate(runID, func() error {
		return f.inner.CompareAndSetPanelPhase(ctx, runID, attemptID, expectedVersion, from, to, synthesis)
	})
}

func (f *abandonFence) SetStepAttemptPrompt(ctx context.Context, runID, attemptID, promptRef string) error {
	return f.mutate(runID, func() error { return f.inner.SetStepAttemptPrompt(ctx, runID, attemptID, promptRef) })
}

func (f *abandonFence) SetStepAttemptExecution(ctx context.Context, runID, attemptID, coordinatorRunID, taskID, reason string) error {
	return f.mutate(runID, func() error {
		return f.inner.SetStepAttemptExecution(ctx, runID, attemptID, coordinatorRunID, taskID, reason)
	})
}

func (f *abandonFence) SetStepAttemptHeartbeat(ctx context.Context, runID, attemptID string, heartbeatAt time.Time) error {
	return f.mutate(runID, func() error {
		return f.inner.SetStepAttemptHeartbeat(ctx, runID, attemptID, heartbeatAt)
	})
}

func (f *abandonFence) ListTransitions(ctx context.Context, runID string) ([]workflowledger.TransitionRecord, error) {
	return f.inner.ListTransitions(ctx, runID)
}

func (f *abandonFence) IncrementLoopCounter(ctx context.Context, runID, loopName string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, abandoned := f.abandoned[runID]; abandoned {
		return 0, workflowledger.ErrConflict
	}
	return f.inner.IncrementLoopCounter(ctx, runID, loopName)
}

func (f *abandonFence) GetLoopCounters(ctx context.Context, runID string) ([]workflowledger.LoopCounter, error) {
	return f.inner.GetLoopCounters(ctx, runID)
}

func (f *abandonFence) CreateApproval(ctx context.Context, a workflowledger.ApprovalRecord) error {
	return f.mutate(a.RunID, func() error { return f.inner.CreateApproval(ctx, a) })
}

func (f *abandonFence) ResolveApproval(ctx context.Context, runID, approvalID, actor, status, reason string) error {
	return f.mutate(runID, func() error { return f.inner.ResolveApproval(ctx, runID, approvalID, actor, status, reason) })
}

func (f *abandonFence) ListApprovals(ctx context.Context, runID string) ([]workflowledger.ApprovalRecord, error) {
	return f.inner.ListApprovals(ctx, runID)
}

func (f *abandonFence) UpsertDelivery(ctx context.Context, d workflowledger.DeliveryRecord) error {
	return f.mutate(d.RunID, func() error { return f.inner.UpsertDelivery(ctx, d) })
}

func (f *abandonFence) GetDeliveryByIdempotencyKey(ctx context.Context, key string) (workflowledger.DeliveryRecord, error) {
	return f.inner.GetDeliveryByIdempotencyKey(ctx, key)
}

func (f *abandonFence) ListDeliveries(ctx context.Context, runID string) ([]workflowledger.DeliveryRecord, error) {
	return f.inner.ListDeliveries(ctx, runID)
}

func (f *abandonFence) ListEvents(ctx context.Context, runID string, limit, offset int) ([]workflowledger.EventRecord, error) {
	return f.inner.ListEvents(ctx, runID, limit, offset)
}

func (f *abandonFence) ClaimRun(ctx context.Context, runID, holder string) error {
	return f.mutate(runID, func() error { return f.inner.ClaimRun(ctx, runID, holder) })
}

func (f *abandonFence) TakeoverRunClaim(ctx context.Context, runID, holder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, abandoned := f.abandoned[runID]; abandoned {
		return workflowledger.ErrClaimHeld
	}
	return f.inner.TakeoverRunClaim(ctx, runID, holder)
}

func (f *abandonFence) TakeoverExpiredRunClaim(ctx context.Context, runID, holder string, maxAge time.Duration) error {
	return f.mutate(runID, func() error { return f.inner.TakeoverExpiredRunClaim(ctx, runID, holder, maxAge) })
}

func (f *abandonFence) ReleaseRun(ctx context.Context, runID, holder string) error {
	return f.mutate(runID, func() error { return f.inner.ReleaseRun(ctx, runID, holder) })
}

func (f *abandonFence) ClearRunClaim(ctx context.Context, runID string) error {
	return f.mutate(runID, func() error { return f.inner.ClearRunClaim(ctx, runID) })
}

func (f *abandonFence) GetRunClaim(ctx context.Context, runID string) (string, time.Time, bool, error) {
	// GetRunClaim is a pure read: it must never abort a mutation. The fence
	// guards WRITES only, so the probe passes through unfenced.
	return f.inner.GetRunClaim(ctx, runID)
}

func (f *abandonFence) StoreContent(ctx context.Context, ref string, data []byte) error {
	if runID, ok := workflowledger.RunIDFromContext(ctx); ok {
		return f.mutate(runID, func() error { return f.inner.StoreContent(ctx, ref, data) })
	}
	return f.inner.StoreContent(ctx, ref, data)
}

func (f *abandonFence) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	return f.inner.LoadContent(ctx, ref)
}

func (f *abandonFence) Recover(ctx context.Context) ([]workflowledger.RecoveredRun, error) {
	return f.inner.Recover(ctx)
}

// Ensure abandonFence implements Repository.
var _ workflowledger.Repository = (*abandonFence)(nil)
