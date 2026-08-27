// Package storage provides the validation seam for durable agent events.
package storage

import (
	"context"
	"errors"
	"time"
)

var (
	ErrDuplicate       = errors.New("duplicate event")
	ErrClaimHeld       = errors.New("run claim held by another holder")
	ErrClaimNotHeld    = errors.New("run claim not held by this holder")
	ErrContentNotFound = errors.New("content not found")
)

// KindRunDeleted is the deletion-tombstone event kind written by
// AppendAndDeleteRun. A run whose only remaining events are tombstones of this
// kind is free for re-admission by AppendBatchForNewRun; any other surviving
// event means the run is still live and admission is refused.
const KindRunDeleted = "run_deleted"

// Claim represents an exclusive execution claim on a run.
type Claim struct {
	RunID      string
	Holder     string
	AcquiredAt string
	Fence      uint64
}

type Event struct {
	ID       string
	RunID    string
	Sequence int
	Kind     string
	Payload  []byte
	// RowID is the event's position in the store's global append order: the
	// SQLite rowid, or the monotone append index on the memory backend. It is
	// set by the store when events are read so a reader can fold events from
	// several runs in the order they were actually appended - in particular a
	// run_deleted tombstone always precedes a later run_created that reuses
	// its idempotency key.
	RowID uint64
}

type Store interface {
	Append(context.Context, Event) error
	// AppendClaimed appends an event when its run is unclaimed or holder owns
	// the current claim. It returns ErrClaimHeld when another holder owns it.
	AppendClaimed(ctx context.Context, event Event, holder string) error
	// AppendAndDeleteRun atomically appends a deletion tombstone and removes
	// prior events and the claim for the same run. The supplied claim authorizes
	// the append when the run has an active claim.
	AppendAndDeleteRun(context.Context, Event, Claim) error
	Events(context.Context, string) ([]Event, error)
	// EventsSince returns the events of a run whose sequence is strictly
	// greater than afterSequence, ordered by ascending sequence. It is the
	// bounded tail read that lets a reader catch up on another writer's
	// appends without replaying the whole history.
	EventsSince(ctx context.Context, runID string, afterSequence int) ([]Event, error)
	// DeleteRun removes events at or below throughSequence and any claim for a
	// run. It never deletes content; a later tombstone event remains visible.
	DeleteRun(ctx context.Context, runID string, throughSequence int) error
	// Changes is the freshness probe for incremental catch-up. Given a cursor
	// previously returned by Changes (0 to start from the beginning), it
	// reports the highest sequence of every run appended to since that cursor,
	// together with the new cursor. Cost is proportional to the number of runs
	// that moved, not to the size of the history, so a caller that is already
	// up to date pays a constant-time probe.
	Changes(ctx context.Context, afterCursor uint64) (maxSequences map[string]int, cursor uint64, err error)
	// ClaimRun acquires an exclusive claim on a run for holder. Returns nil
	// if the claim was acquired. Returns ErrClaimHeld if another holder
	// already holds the claim. The same holder calling ClaimRun again
	// refreshes the claim successfully.
	ClaimRun(ctx context.Context, runID, holder string) error
	// TakeoverClaim atomically replaces any existing claim with holder.
	TakeoverClaim(ctx context.Context, runID, holder string) error
	// ReleaseClaim releases the claim on a run. Only the current holder may
	// release. Returns ErrClaimNotHeld if the caller does not hold the claim.
	ReleaseClaim(ctx context.Context, runID, holder string) error
	// ClearClaim force-releases any claim on a run, regardless of holder.
	// Returns nil if no claim existed. Used during crash recovery to clear
	// stale claims on terminal runs.
	ClearClaim(ctx context.Context, runID string) error
	// PutContent stores raw bytes keyed by a content-addressed reference
	// (e.g. "ref:output:xxxx"). Idempotent for the same ref.
	PutContent(ctx context.Context, ref string, data []byte) error
	// GetContent retrieves bytes previously stored by PutContent.
	// Returns ErrContentNotFound if the ref is unknown.
	GetContent(ctx context.Context, ref string) ([]byte, error)
	Count(context.Context) (int, error)
	ListRunIDs(context.Context) ([]string, error)
	Close() error
}

// ExistingClaimAppender appends only when holder owns an existing claim.
// Unlike Store.AppendClaimed, an unclaimed run is refused.
type ExistingClaimAppender interface {
	AppendWithExistingClaim(context.Context, Event, string) error
}

// BatchAppender atomically appends a set of events.
type BatchAppender interface {
	AppendBatch(context.Context, []Event) error
}

// NewRunBatchAppender atomically appends a new-run batch only when no event
// or claim exists for its run. It is the atomic admission boundary.
type NewRunBatchAppender interface {
	AppendBatchForNewRun(context.Context, string, []Event) error
}

// LeaseStore is the optional extension used by workflow recovery.
type LeaseStore interface {
	TakeoverExpiredClaim(context.Context, string, string, time.Duration) error
}

// ClaimReader is the optional extension for reading a run's current execution
// claim without mutating it. It backs read-only liveness probes (sidebar claim
// age, delivery_pending heartbeat); a backend that cannot expose claims simply
// does not implement it.
type ClaimReader interface {
	// GetClaim returns the current claim for runID. ErrClaimNotHeld when the
	// run has no claim.
	GetClaim(context.Context, string) (Claim, error)
}

// FencedLeaseStore guards stale writes after an expired claim changes owner.
type FencedLeaseStore interface {
	ClaimRunFenced(context.Context, string, string) (Claim, error)
	TakeoverExpiredClaimFenced(context.Context, string, string, time.Duration) (Claim, error)
	// TakeoverClaimFenced atomically replaces any existing claim with holder,
	// bumping the claim fence so a prior holder's captured fence no longer
	// authorizes writes, and returning the new claim.
	TakeoverClaimFenced(context.Context, string, string) (Claim, error)
	// RefreshClaimFenced refreshes the claim's acquired_at ONLY when holder
	// already owns the claim row, returning that claim. It never inserts a
	// missing row: a holder whose claim is gone is reported as
	// ErrClaimNotHeld, so a displaced/expired holder cannot reclaim itself
	// through the heartbeat.
	RefreshClaimFenced(context.Context, string, string) (Claim, error)
	AppendClaimedFenced(context.Context, Event, Claim) error
	ReleaseClaimFenced(context.Context, Claim) error
}
