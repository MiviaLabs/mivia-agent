package ledger

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledgercore"
)

func (s *StorageLedgerRepository) ClaimRun(ctx context.Context, runID string, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.claims.ClaimRun(ctx, s.store, runID, holder)
}

func (s *StorageLedgerRepository) ReleaseRun(ctx context.Context, runID string, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.claims.ReleaseRun(ctx, s.store, runID, holder)
}

func (s *StorageLedgerRepository) TakeoverExpiredRunClaim(ctx context.Context, runID, holder string, maxAge time.Duration) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.claims.TakeoverExpiredRunClaim(ctx, s.store, runID, holder, maxAge)
}

func (s *StorageLedgerRepository) ClearRunClaim(ctx context.Context, runID string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.claims.ClearRunClaim(ctx, s.store, runID)
}

// IsRunHeld reports whether runID currently has an active claim. The probe is
// read-only: it never acquires, refreshes, or releases a claim, so observing
// a run can never disturb its holder. Backends that cannot expose claim state
// report (false, nil) instead of failing.
func (s *StorageLedgerRepository) IsRunHeld(ctx context.Context, runID string) (bool, error) {
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
func (s *StorageLedgerRepository) IsRunTokenFenced(ctx context.Context, runID, token string) (bool, error) {
	if err := s.checkOpen(); err != nil {
		return false, err
	}
	return s.claims.IsRunTokenFenced(ctx, s.store, runID, token)
}

func (s *StorageLedgerRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return ledgercore.StoreContent(ctx, s.store, ref, data)
}

func (s *StorageLedgerRepository) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return ledgercore.LoadContent(ctx, s.store, ref)
}
