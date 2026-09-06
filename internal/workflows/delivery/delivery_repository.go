package delivery

import (
	"context"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// LedgerRepository is this package's consumer-side view of the workflow
// ledger: the run-read, attempt, content, and delivery members the publish,
// repair, stacking, and follow-up paths use. The full ledger contract carries
// loop counters, approvals, panel phases, events, and admin members this
// package never touches; it depends on the subset, not the fat interface.
// workflowLedgerRepository satisfies it.
type LedgerRepository interface {
	GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error)
	GetRunSnapshot(ctx context.Context, runID string) ([]byte, error)
	CompareAndSetRunStatus(ctx context.Context, runID string, expectedVersion uint64, status workflowledger.RunStatus, finishedAt *time.Time) error
	CreateRun(ctx context.Context, snap workflowledger.RunSnapshot, snapshotJSON []byte) error
	CreateStepAttempt(ctx context.Context, attempt workflowledger.StepAttempt) error
	GetStepAttempt(ctx context.Context, runID, attemptID string) (workflowledger.StepAttempt, error)
	ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error)
	CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error
	RecordStepAttemptOutcome(ctx context.Context, attempt workflowledger.StepAttempt, outcome workflowledger.AttemptOutcome) error
	UpsertDelivery(ctx context.Context, d workflowledger.DeliveryRecord) error
	GetDeliveryByIdempotencyKey(ctx context.Context, key string) (workflowledger.DeliveryRecord, error)
	ListDeliveries(ctx context.Context, runID string) ([]workflowledger.DeliveryRecord, error)
	StoreContent(ctx context.Context, ref string, data []byte) error
	LoadContent(ctx context.Context, ref string) ([]byte, error)
}

// Compile-time check that the shipped ledger repository satisfies this subset.
var _ LedgerRepository = (*workflowledger.StorageRepository)(nil)
