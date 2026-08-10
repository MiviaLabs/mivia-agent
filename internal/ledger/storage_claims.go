package ledger

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// newHolderID generates a random per-process identifier for run execution
// claims. It is never a principal, session ID or role - see plan 12 §3.
//
// crypto/rand.Read never returns an error and always fills its buffer, crashing
// the program itself if the operating system's source fails, so there is no
// error to handle. A holder ID from a degraded source would be guessable, and
// no fallback source is safe to substitute.
func newHolderID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "h-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}

func (s *StorageLedgerRepository) ClaimRun(ctx context.Context, runID string, holder string) error {
	claim := storage.Claim{RunID: runID, Holder: holder}
	var err error
	if fenced, ok := s.store.(storage.FencedLeaseStore); ok {
		claim, err = fenced.ClaimRunFenced(ctx, runID, holder)
	} else {
		err = s.store.ClaimRun(ctx, runID, holder)
	}
	if err != nil {
		if errors.Is(err, storage.ErrClaimHeld) {
			return ErrClaimHeld
		}
		return err
	}
	s.mu.Lock()
	s.claimedRuns[runID] = claim
	s.mu.Unlock()
	return nil
}

func (s *StorageLedgerRepository) ReleaseRun(ctx context.Context, runID string, holder string) error {
	s.mu.RLock()
	claim := s.claimedRuns[runID]
	s.mu.RUnlock()
	var err error
	if fenced, ok := s.store.(storage.FencedLeaseStore); ok && claim.Fence != 0 {
		err = fenced.ReleaseClaimFenced(ctx, claim)
	} else {
		err = s.store.ReleaseClaim(ctx, runID, holder)
	}
	if err != nil {
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

func (s *StorageLedgerRepository) TakeoverExpiredRunClaim(ctx context.Context, runID, holder string, maxAge time.Duration) error {
	fenced, ok := s.store.(storage.FencedLeaseStore)
	if !ok {
		return ErrClaimHeld
	}
	claim, err := fenced.TakeoverExpiredClaimFenced(ctx, runID, holder, maxAge)
	if errors.Is(err, storage.ErrClaimHeld) {
		return ErrClaimHeld
	}
	if errors.Is(err, storage.ErrClaimNotHeld) {
		return ErrClaimNotHeld
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.claimedRuns[runID] = claim
	s.mu.Unlock()
	return nil
}

func (s *StorageLedgerRepository) ClearRunClaim(ctx context.Context, runID string) error {
	if err := s.store.ClearClaim(ctx, runID); err != nil {
		return err
	}
	// Mirror the workflows ledger: drop the in-memory holder so this instance
	// stops claiming the run (its subsequent fenced writes fail closed).
	s.mu.Lock()
	delete(s.claimedRuns, runID)
	s.mu.Unlock()
	return nil
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
