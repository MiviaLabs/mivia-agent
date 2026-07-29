package ledger

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// newHolderID generates a random per-process identifier for run execution
// claims. It is never a principal, session ID or role — see plan 12 §3.
func newHolderID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("generate holder ID: %v", err))
	}
	return "h-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}

func (s *StorageLedgerRepository) ClaimRun(ctx context.Context, runID string, holder string) error {
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

func (s *StorageLedgerRepository) ReleaseRun(ctx context.Context, runID string, holder string) error {
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

func (s *StorageLedgerRepository) ClearRunClaim(ctx context.Context, runID string) error {
	return s.store.ClearClaim(ctx, runID)
}

func (s *StorageLedgerRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	return s.store.PutContent(ctx, ref, data)
}

func (s *StorageLedgerRepository) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	data, err := s.store.GetContent(ctx, ref)
	if err != nil {
		if errors.Is(err, storage.ErrContentNotFound) {
			return nil, ErrContentNotFound
		}
		return nil, err
	}
	return data, nil
}
