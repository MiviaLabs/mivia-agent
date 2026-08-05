package ledger

import (
	"context"
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// ClaimRun acquires the exclusive execution claim on a run. Returns
// ErrClaimHeld if another holder owns it. Same-holder refresh succeeds.
func (s *StorageRepository) ClaimRun(ctx context.Context, runID, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if holder == "" {
		return ErrClaimNotHeld
	}
	if err := s.store.ClaimRun(ctx, runID, holder); err != nil {
		if errors.Is(err, storage.ErrClaimHeld) {
			return ErrClaimHeld
		}
		return err
	}
	s.mu.Lock()
	s.claimedRuns[runID] = holder
	s.mu.Unlock()
	return nil
}

// ReleaseRun releases the claim. Only the current holder may release it.
func (s *StorageRepository) ReleaseRun(ctx context.Context, runID, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := s.store.ReleaseClaim(ctx, runID, holder); err != nil {
		if errors.Is(err, storage.ErrClaimNotHeld) {
			return ErrClaimNotHeld
		}
		return err
	}
	s.mu.Lock()
	delete(s.claimedRuns, runID)
	s.mu.Unlock()
	return nil
}

// ClearRunClaim removes a run claim. It is for an operator force release.
func (s *StorageRepository) ClearRunClaim(ctx context.Context, runID string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := s.store.ClearClaim(ctx, runID); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.claimedRuns, runID)
	s.mu.Unlock()
	return nil
}

// StoreContent persists bytes under a content-addressed reference.
func (s *StorageRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.store.PutContent(ctx, ref, data)
}

// LoadContent retrieves stored bytes. It returns ErrContentNotFound if absent.
func (s *StorageRepository) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	data, err := s.store.GetContent(ctx, ref)
	if err != nil {
		if errors.Is(err, storage.ErrContentNotFound) {
			return nil, ErrContentNotFound
		}
		return nil, err
	}
	return data, nil
}

// Ensure StorageRepository implements Repository at compile time.
var _ Repository = (*StorageRepository)(nil)
