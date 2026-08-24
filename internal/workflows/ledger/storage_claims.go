package ledger

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
)

// ClaimRun acquires the exclusive execution claim on a run. Returns
// ErrClaimHeld if another holder owns it. Same-holder refresh succeeds.
func (s *StorageRepository) ClaimRun(ctx context.Context, runID, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.claims.ClaimRun(ctx, s.store, runID, holder)
}

// RefreshRunClaim refreshes the claim's acquired_at ONLY when this repository
// already holds the claim row. It never inserts a missing row. When the row is
// gone or owned by another holder it returns ErrClaimNotHeld, the signal a
// heartbeat uses to treat the claim as lost and stop executing (F2).
func (s *StorageRepository) RefreshRunClaim(ctx context.Context, runID, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.claims.RefreshRunClaim(ctx, s.store, runID, holder)
}

// TakeoverRunClaim atomically replaces any existing execution claim. The
// replacement bumps the claim fence so a previous holder's captured fence no
// longer authorizes writes.
func (s *StorageRepository) TakeoverRunClaim(ctx context.Context, runID, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.claims.TakeoverRunClaim(ctx, s.store, runID, holder)
}

// TakeoverExpiredRunClaim replaces a claim only when its age exceeds maxAge.
func (s *StorageRepository) TakeoverExpiredRunClaim(ctx context.Context, runID, holder string, maxAge time.Duration) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.claims.TakeoverExpiredRunClaim(ctx, s.store, runID, holder, maxAge)
}

// ReleaseRun releases the claim. Only the current holder may release it. The
// release is fenced: it removes the claim only while this repository's stored
// (holder, fence) still matches the live row, so a stale release can never
// strip a newer holder's claim.
func (s *StorageRepository) ReleaseRun(ctx context.Context, runID, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.claims.ReleaseRun(ctx, s.store, runID, holder)
}

// ClearRunClaim removes a run claim. It is for an operator force release.
func (s *StorageRepository) ClearRunClaim(ctx context.Context, runID string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.claims.ClearRunClaim(ctx, s.store, runID)
}

// GetRunClaim reads the run's current execution claim as a pure liveness
// probe: the holder and the claim's last acquired_at (the holder refreshes
// acquired_at on every heartbeat, so it is the claim's liveness tick). It
// never acquires, refreshes, or releases a claim, so observing a run can
// never disturb its holder. ok=false means the run has no claim (or the
// backend cannot expose claims); err is reserved for backend failures and
// means the caller must not trust ok.
func (s *StorageRepository) GetRunClaim(ctx context.Context, runID string) (holder string, acquiredAt time.Time, ok bool, err error) {
	if err := s.checkOpen(); err != nil {
		return "", time.Time{}, false, err
	}
	return s.claims.GetRunClaim(ctx, s.store, runID)
}

// IsRunHeld reports whether runID currently has an active claim. The probe is
// read-only: it never acquires, refreshes, or releases a claim, so observing
// a run can never disturb its holder. Backends that cannot expose claim state
// report (false, nil) instead of failing.
func (s *StorageRepository) IsRunHeld(ctx context.Context, runID string) (bool, error) {
	if err := s.checkOpen(); err != nil {
		return false, err
	}
	return s.claims.IsRunHeld(ctx, s.store, runID)
}

// IsRunTokenFenced reports whether token has been fenced out of runID by a
// subsequent takeover. The history is durable: a fenced token stays fenced
// across releases, so a re-issued claim by the same token reads true until
// cleanup. A token that is the current holder of runID always reads false.
// Backends that cannot expose fence history report (false, nil).
func (s *StorageRepository) IsRunTokenFenced(ctx context.Context, runID, token string) (bool, error) {
	if err := s.checkOpen(); err != nil {
		return false, err
	}
	return s.claims.IsRunTokenFenced(ctx, s.store, runID, token)
}

// StoreContent persists bytes under a content-addressed reference.
func (s *StorageRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return ledgercore.StoreContent(ctx, s.store, ref, data)
}

// LoadContent retrieves stored bytes. It returns ErrContentNotFound if absent.
func (s *StorageRepository) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return ledgercore.LoadContent(ctx, s.store, ref)
}

// parseClaimAcquiredAt parses a claim's acquired_at timestamp.
var parseClaimAcquiredAt = ledgercore.ParseClaimAcquiredAt

// Ensure StorageRepository implements Repository at compile time.
var _ Repository = (*StorageRepository)(nil)
