package ledger

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors returned by Repository methods.
var (
	ErrDuplicate         = errors.New("duplicate record")
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("state conflict")
	ErrInvalidTransition = errors.New("invalid state transition")
	ErrClaimHeld         = errors.New("run claim held by another executor")
	ErrClaimNotHeld      = errors.New("run claim not held by this executor")
	ErrClosed            = errors.New("repository is closed")
	ErrContentNotFound   = errors.New("content not found")
)

// RecoveredRun summarises one workflow run for the startup recovery report.
type RecoveredRun struct {
	RunID          string
	WorkflowName   string
	Status         RunStatus
	WasInterrupted bool
	CreatedAt      time.Time
}

// Repository is the durable storage boundary for workflow runs. Implementations
// must be concurrency-safe and return defensive copies.
//
// Concurrency contract: mutations are serialized per run. When a caller holds
// an execution claim (ClaimRun), only that holder can mutate the run. The
// repository enforces intra-process serialization and claim fencing. CAS
// methods take the caller's observed version and fail with
// ErrConflict when the recorded version has moved.
type Repository interface {
	// CreateRun admits a run: persists the run snapshot (typed fields + the
	// canonical snapshot JSON) and records the wf_run_created event. Returns
	// ErrDuplicate if the run already exists, ErrInvalidTransition if the
	// snapshot status is not pending.
	CreateRun(ctx context.Context, snap RunSnapshot, snapshotJSON []byte) error

	// GetRun returns the current run snapshot with the DERIVED active step
	// (see Projection.ActiveStepID). Returns ErrNotFound if absent.
	GetRun(ctx context.Context, runID string) (RunSnapshot, error)

	// ListRuns returns bounded snapshots, optionally filtered by status.
	ListRuns(ctx context.Context, status ...RunStatus) ([]RunSnapshot, error)

	// GetRunSnapshot returns the canonical snapshot JSON stored at admission.
	// Returns ErrNotFound if absent.
	GetRunSnapshot(ctx context.Context, runID string) ([]byte, error)

	// CompareAndSetRunStatus atomically transitions the run status, bumping
	// the run version. Returns ErrConflict on version mismatch, ErrInvalidTransition
	// on an illegal edge. finishedAt is persisted when the new status is terminal.
	CompareAndSetRunStatus(ctx context.Context, runID string, expectedVersion uint64, status RunStatus, finishedAt *time.Time) error

	// CreateStepAttempt records a fresh numbered attempt for a step. The
	// (runID, stepID, attemptNo) triple is unique: a second create for the
	// same triple never appends a second event (ErrDuplicate in-process, or
	// ErrConflict when a concurrent writer took the deterministic event ID).
	CreateStepAttempt(ctx context.Context, attempt StepAttempt) error

	// GetStepAttempt returns one attempt. Returns ErrNotFound if absent.
	GetStepAttempt(ctx context.Context, runID, attemptID string) (StepAttempt, error)

	// ListStepAttempts returns the run's attempts ordered by event sequence.
	ListStepAttempts(ctx context.Context, runID string) ([]StepAttempt, error)

	// CompleteStepAttempt atomically records an attempt's terminal outcome
	// (status + optional route/output evidence in ONE event) under CAS on the
	// attempt version. Returns ErrConflict on version mismatch, ErrInvalidTransition
	// for a non-terminal outcome status or an illegal status edge.
	CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome AttemptOutcome) error

	// SetStepAttemptPrompt records the content-addressed prompt reference for
	// one attempt (the prompt body lives in content-addressed storage and is
	// looked up via PromptRef; the event log never carries prompt text). The
	// attempt may still be Running — the prompt is persisted at dispatch time,
	// before completion — and its status/version are never changed. Setting the
	// same promptRef twice is an idempotent no-op. Returns ErrNotFound if the
	// run or attempt is absent; ErrConflict if the attempt already carries a
	// prompt ref different from promptRef (attempts are immutable after
	// dispatch) or a concurrent writer took the deterministic event ID with a
	// different payload.
	SetStepAttemptPrompt(ctx context.Context, runID, attemptID, promptRef string) error

	// ListTransitions returns the route decisions derived from completed
	// attempts, ordered by event sequence.
	ListTransitions(ctx context.Context, runID string) ([]TransitionRecord, error)

	// IncrementLoopCounter mints the next iteration number for a named loop
	// under the run claim, after catch-up. Counters are derived state: the
	// returned number is persisted via a wf_loop_incremented event and rebuilt
	// on reopen. Returns ErrNotFound if the run is absent.
	IncrementLoopCounter(ctx context.Context, runID, loopName string) (int, error)

	// GetLoopCounters returns the run's derived loop counters.
	GetLoopCounters(ctx context.Context, runID string) ([]LoopCounter, error)

	// CreateApproval records a pending human-gate request (provisional).
	CreateApproval(ctx context.Context, a ApprovalRecord) error

	// ResolveApproval resolves a pending approval to approved or rejected.
	ResolveApproval(ctx context.Context, runID, approvalID, actor, status, reason string) error

	// ListApprovals returns the run's approval records.
	ListApprovals(ctx context.Context, runID string) ([]ApprovalRecord, error)

	// UpsertDelivery records a delivery attempt keyed by idempotency key.
	UpsertDelivery(ctx context.Context, d DeliveryRecord) error

	// GetDeliveryByIdempotencyKey returns the delivery record for a key.
	// Returns ErrNotFound if absent.
	GetDeliveryByIdempotencyKey(ctx context.Context, key string) (DeliveryRecord, error)

	// ListDeliveries returns the run's delivery records.
	ListDeliveries(ctx context.Context, runID string) ([]DeliveryRecord, error)

	// ListEvents returns the run's audit trail, ordered by event sequence,
	// paged (limit <= 0 means DefaultEventPageSize, offset skips events).
	// Summaries are bounded and never contain raw payloads. Unknown kinds
	// and undecodable payloads are skipped. Returns ErrNotFound when the
	// run is absent.
	ListEvents(ctx context.Context, runID string, limit, offset int) ([]EventRecord, error)

	// ClaimRun acquires the exclusive execution claim on a run. Returns
	// ErrClaimHeld if another holder owns it. Same-holder refresh succeeds.
	ClaimRun(ctx context.Context, runID, holder string) error

	// TakeoverRunClaim atomically replaces any existing claim with holder.
	TakeoverRunClaim(ctx context.Context, runID, holder string) error

	// ReleaseRun releases the claim; only the current holder may. Returns
	// ErrClaimNotHeld otherwise.
	ReleaseRun(ctx context.Context, runID, holder string) error

	// ClearRunClaim force-releases any claim regardless of holder (explicit
	// operator force-release for stale claims; Recover clears claims only on
	// terminal runs).
	ClearRunClaim(ctx context.Context, runID string) error

	// StoreContent persists bytes under a content-addressed reference
	// (shared content store; idempotent).
	StoreContent(ctx context.Context, ref string, data []byte) error

	// LoadContent retrieves stored bytes. Returns ErrContentNotFound if absent.
	LoadContent(ctx context.Context, ref string) ([]byte, error)

	// Recover brings the projection up to date, classifies every run, and
	// clears stale claims on terminal runs only. It mutates no run status.
	Recover(ctx context.Context) ([]RecoveredRun, error)
}
