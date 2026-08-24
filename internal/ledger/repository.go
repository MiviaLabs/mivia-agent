package ledger

import (
	"context"
	"errors"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
)

// Sentinel errors returned by LedgerRepository methods.
var (
	ErrDuplicate         = ledgercore.ErrDuplicate
	ErrNotFound          = ledgercore.ErrNotFound
	ErrInvalidTransition = ledgercore.ErrInvalidTransition
	ErrConflict          = ledgercore.ErrConflict
	ErrClosed            = ledgercore.ErrClosed
	ErrInvalidReference  = errors.New("invalid ledger reference")
	ErrClaimHeld         = ledgercore.ErrClaimHeld
	ErrClaimNotHeld      = ledgercore.ErrClaimNotHeld
	ErrContentNotFound   = ledgercore.ErrContentNotFound
)

// LedgerRepository is the narrow storage boundary for the coordinator.
// Implementations must be concurrency-safe and return defensive copies.
type LedgerRepository interface {
	AdmitSingleTask(context.Context, SingleTaskAdmission) error
	// CreateRun creates a new run record. Returns ErrDuplicate if an
	// idempotency-key matched run already exists.
	CreateRun(ctx context.Context, key string, snapshot RunSnapshot) error

	// GetRun returns a defensive copy of the current run snapshot.
	// Returns ErrNotFound if the run does not exist.
	GetRun(ctx context.Context, runID string) (RunSnapshot, error)

	// GetRunByIdempotencyKey returns the run previously created with key.
	// Returns ErrNotFound for an empty or unknown key.
	GetRunByIdempotencyKey(ctx context.Context, key string) (RunSnapshot, error)

	// ListRuns returns bounded snapshots, optionally filtered by status.
	ListRuns(ctx context.Context, status ...RunStatus) ([]RunSnapshot, error)

	// CreateTask creates a new task record within a run.
	// Returns ErrDuplicate if the task ID already exists.
	// Returns ErrNotFound if the run does not exist.
	// Returns ErrClosed if the run has been closed/deleted.
	CreateTask(ctx context.Context, snap TaskSnapshot) error

	// GetTask returns a defensive copy of a task snapshot.
	// Returns ErrNotFound if the task does not exist.
	GetTask(ctx context.Context, runID, taskID string) (TaskSnapshot, error)

	// ListTasks returns all task snapshots for a run, ordered by creation.
	ListTasks(ctx context.Context, runID string) ([]TaskSnapshot, error)

	// AppendEvent records a lifecycle event with idempotency-key dedup.
	// Returns ErrDuplicate if the event ID already exists.
	AppendEvent(ctx context.Context, event LifecycleEvent) error

	// ListEvents returns all events for a run, ordered by sequence.
	ListEvents(ctx context.Context, runID string) ([]LifecycleEvent, error)

	// CompareAndSetTaskStatus atomically transitions a task's status and
	// increments its version. Returns ErrConflict if the current version
	// does not match expectedVersion. Returns ErrInvalidTransition if
	// the status change is not valid.
	CompareAndSetTaskStatus(ctx context.Context, runID, taskID string,
		expectedVersion uint64, newStatus string) error

	// SetTaskOutput stores a bounded redacted output/error reference for a task.
	SetTaskOutput(ctx context.Context, runID, taskID string,
		outputRef, errorRef string) error

	// SetTaskAttempt records the terminal state of one persisted attempt.
	// An attempt ID that is not yet present starts a new attempt rather than
	// erroring, so a re-execution (a retry, or a resumed run) records its own
	// outcome instead of overwriting the record of the execution before it.
	SetTaskAttempt(ctx context.Context, runID, taskID, attemptID, status string,
		finishedAt *time.Time) error

	// CloseRun marks a run as closed. No further state transitions are allowed.
	// Returns ErrNotFound if the run does not exist.
	// Returns ErrInvalidTransition if already closed.
	CloseRun(ctx context.Context, runID string) error

	// DeleteRun removes all data for a run. Returns ErrNotFound if not found.
	DeleteRun(ctx context.Context, runID string) error

	// ClaimRun acquires an exclusive execution claim on a run. The holder is
	// a random per-process ID - never a principal, session ID or role. Returns
	// ErrClaimHeld if another holder already holds the claim. The same holder
	// calling ClaimRun again refreshes the claim successfully.
	ClaimRun(ctx context.Context, runID, holder string) error

	// ReleaseRun releases the execution claim on a run. Only the current
	// holder may release. Returns ErrClaimNotHeld if the caller does not hold
	// the claim.
	ReleaseRun(ctx context.Context, runID, holder string) error

	// ClearRunClaim force-releases any execution claim on a run, regardless
	// of holder. Used during crash recovery to clear stale claims on runs
	// that have reached a terminal state.
	ClearRunClaim(ctx context.Context, runID string) error

	// StoreContent persists raw bytes keyed by a content-addressed reference
	// (e.g. "ref:output:xxxx"). The same ref may be stored multiple times;
	// subsequent stores are idempotent. Recorded content is never reclaimed,
	// including when the run that stored it is deleted.
	StoreContent(ctx context.Context, ref string, data []byte) error

	// LoadContent retrieves bytes previously stored by StoreContent.
	// Returns ErrContentNotFound if the ref is unknown.
	LoadContent(ctx context.Context, ref string) ([]byte, error)
}

// LeaseRepository can take a claim only after its heartbeat expires.
type LeaseRepository interface {
	TakeoverExpiredRunClaim(context.Context, string, string, time.Duration) error
}
