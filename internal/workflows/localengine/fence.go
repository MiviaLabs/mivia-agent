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
	_, ok := f.abandoned[runID]
	f.mu.Unlock()
	return ok
}

func (f *abandonFence) CreateRun(ctx context.Context, snap workflowledger.RunSnapshot, snapshotJSON []byte) error {
	if f.isAbandoned(snap.RunID) {
		return workflowledger.ErrConflict
	}
	return f.inner.CreateRun(ctx, snap, snapshotJSON)
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
	if f.isAbandoned(runID) {
		return workflowledger.ErrConflict
	}
	return f.inner.CompareAndSetRunStatus(ctx, runID, expectedVersion, status, finishedAt)
}

func (f *abandonFence) CreateStepAttempt(ctx context.Context, attempt workflowledger.StepAttempt) error {
	if f.isAbandoned(attempt.RunID) {
		return workflowledger.ErrConflict
	}
	return f.inner.CreateStepAttempt(ctx, attempt)
}

func (f *abandonFence) GetStepAttempt(ctx context.Context, runID, attemptID string) (workflowledger.StepAttempt, error) {
	return f.inner.GetStepAttempt(ctx, runID, attemptID)
}

func (f *abandonFence) ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error) {
	return f.inner.ListStepAttempts(ctx, runID)
}

func (f *abandonFence) CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error {
	if f.isAbandoned(runID) {
		return workflowledger.ErrConflict
	}
	return f.inner.CompleteStepAttempt(ctx, runID, attemptID, expectedVersion, outcome)
}

func (f *abandonFence) CompareAndSetPanelPhase(ctx context.Context, runID, attemptID string, expectedVersion uint64, from workflowledger.PanelPhase, to workflowledger.PanelPhase, synthesis *workflowledger.PanelSynthesisExecution) error {
	if f.isAbandoned(runID) {
		return workflowledger.ErrConflict
	}
	return f.inner.CompareAndSetPanelPhase(ctx, runID, attemptID, expectedVersion, from, to, synthesis)
}

func (f *abandonFence) SetStepAttemptPrompt(ctx context.Context, runID, attemptID, promptRef string) error {
	if f.isAbandoned(runID) {
		return workflowledger.ErrConflict
	}
	return f.inner.SetStepAttemptPrompt(ctx, runID, attemptID, promptRef)
}

func (f *abandonFence) SetStepAttemptExecution(ctx context.Context, runID, attemptID, coordinatorRunID, taskID string) error {
	if f.isAbandoned(runID) {
		return workflowledger.ErrConflict
	}
	return f.inner.SetStepAttemptExecution(ctx, runID, attemptID, coordinatorRunID, taskID)
}

func (f *abandonFence) ListTransitions(ctx context.Context, runID string) ([]workflowledger.TransitionRecord, error) {
	return f.inner.ListTransitions(ctx, runID)
}

func (f *abandonFence) IncrementLoopCounter(ctx context.Context, runID, loopName string) (int, error) {
	if f.isAbandoned(runID) {
		return 0, workflowledger.ErrConflict
	}
	return f.inner.IncrementLoopCounter(ctx, runID, loopName)
}

func (f *abandonFence) GetLoopCounters(ctx context.Context, runID string) ([]workflowledger.LoopCounter, error) {
	return f.inner.GetLoopCounters(ctx, runID)
}

func (f *abandonFence) CreateApproval(ctx context.Context, a workflowledger.ApprovalRecord) error {
	if f.isAbandoned(a.RunID) {
		return workflowledger.ErrConflict
	}
	return f.inner.CreateApproval(ctx, a)
}

func (f *abandonFence) ResolveApproval(ctx context.Context, runID, approvalID, actor, status, reason string) error {
	if f.isAbandoned(runID) {
		return workflowledger.ErrConflict
	}
	return f.inner.ResolveApproval(ctx, runID, approvalID, actor, status, reason)
}

func (f *abandonFence) ListApprovals(ctx context.Context, runID string) ([]workflowledger.ApprovalRecord, error) {
	return f.inner.ListApprovals(ctx, runID)
}

func (f *abandonFence) UpsertDelivery(ctx context.Context, d workflowledger.DeliveryRecord) error {
	if f.isAbandoned(d.RunID) {
		return workflowledger.ErrConflict
	}
	return f.inner.UpsertDelivery(ctx, d)
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
	if f.isAbandoned(runID) {
		return workflowledger.ErrConflict
	}
	return f.inner.ClaimRun(ctx, runID, holder)
}

func (f *abandonFence) TakeoverRunClaim(ctx context.Context, runID, holder string) error {
	if f.isAbandoned(runID) {
		return workflowledger.ErrClaimHeld
	}
	return f.inner.TakeoverRunClaim(ctx, runID, holder)
}

func (f *abandonFence) TakeoverExpiredRunClaim(ctx context.Context, runID, holder string, maxAge time.Duration) error {
	if f.isAbandoned(runID) {
		return workflowledger.ErrConflict
	}
	return f.inner.TakeoverExpiredRunClaim(ctx, runID, holder, maxAge)
}

func (f *abandonFence) ReleaseRun(ctx context.Context, runID, holder string) error {
	if f.isAbandoned(runID) {
		return workflowledger.ErrConflict
	}
	return f.inner.ReleaseRun(ctx, runID, holder)
}

func (f *abandonFence) ClearRunClaim(ctx context.Context, runID string) error {
	if f.isAbandoned(runID) {
		return workflowledger.ErrConflict
	}
	return f.inner.ClearRunClaim(ctx, runID)
}

func (f *abandonFence) StoreContent(ctx context.Context, ref string, data []byte) error {
	if runID, ok := workflowledger.RunIDFromContext(ctx); ok && f.isAbandoned(runID) {
		return workflowledger.ErrConflict
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
