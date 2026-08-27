package ledger

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
)

// ClaimRun acquires the exclusive execution claim on a run. Returns
// ErrClaimHeld if another holder owns it. Same-holder refresh succeeds.
func (s *StorageRepository) ClaimRun(ctx context.Context, runID, holder string) error {
	return s.engine.ClaimRun(ctx, runID, holder)
}

// RefreshRunClaim refreshes the claim's acquired_at ONLY when this repository
// already holds the claim row.
func (s *StorageRepository) RefreshRunClaim(ctx context.Context, runID, holder string) error {
	return s.engine.RefreshRunClaim(ctx, runID, holder)
}

// TakeoverRunClaim atomically replaces any existing execution claim.
func (s *StorageRepository) TakeoverRunClaim(ctx context.Context, runID, holder string) error {
	return s.engine.TakeoverRunClaim(ctx, runID, holder)
}

// TakeoverExpiredRunClaim replaces a claim only when its age exceeds maxAge.
func (s *StorageRepository) TakeoverExpiredRunClaim(ctx context.Context, runID, holder string, maxAge time.Duration) error {
	return s.engine.TakeoverExpiredRunClaim(ctx, runID, holder, maxAge)
}

// ReleaseRun releases the claim. Only the current holder may release it.
func (s *StorageRepository) ReleaseRun(ctx context.Context, runID, holder string) error {
	return s.engine.ReleaseRun(ctx, runID, holder)
}

// ClearRunClaim removes a run claim for an operator force release.
func (s *StorageRepository) ClearRunClaim(ctx context.Context, runID string) error {
	return s.engine.ClearRunClaim(ctx, runID)
}

// GetRunClaim reads the run's current execution claim as a pure liveness probe.
func (s *StorageRepository) GetRunClaim(ctx context.Context, runID string) (holder string, acquiredAt time.Time, ok bool, err error) {
	return s.engine.GetRunClaim(ctx, runID)
}

// IsRunHeld reports whether runID currently has an active claim.
func (s *StorageRepository) IsRunHeld(ctx context.Context, runID string) (bool, error) {
	return s.engine.IsRunHeld(ctx, runID)
}

// IsRunTokenFenced reports whether token has been fenced out of runID.
func (s *StorageRepository) IsRunTokenFenced(ctx context.Context, runID, token string) (bool, error) {
	return s.engine.IsRunTokenFenced(ctx, runID, token)
}

// StoreContent persists bytes under a content-addressed reference.
func (s *StorageRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	return s.engine.StoreContent(ctx, ref, data)
}

// LoadContent retrieves stored bytes. It returns ErrContentNotFound if absent.
func (s *StorageRepository) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	return s.engine.LoadContent(ctx, ref)
}

// parseClaimAcquiredAt parses a claim's acquired_at timestamp.
var parseClaimAcquiredAt = ledgercore.ParseClaimAcquiredAt

// Ensure StorageRepository implements Repository at compile time.
var _ Repository = (*StorageRepository)(nil)
