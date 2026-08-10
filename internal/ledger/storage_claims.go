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
	// The fenced store insert and the closed check share ONE critical section:
	// either the insert commits and Close's claim snapshot collects and
	// releases it before the store closes, or Close wins and the claim is
	// refused before it touches the store. Without this serialization, a claim
	// acquired while Close runs is released by the closed-path compensation
	// against an already-closed store, the release error is discarded, and the
	// claim row survives the shutdown (a dead owner's fresh claim that blocks
	// the next process for the lease duration).
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if fenced, ok := s.store.(storage.FencedLeaseStore); ok {
		claim, err = fenced.ClaimRunFenced(ctx, runID, holder)
	} else {
		err = s.store.ClaimRun(ctx, runID, holder)
	}
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, storage.ErrClaimHeld) {
			return ErrClaimHeld
		}
		return err
	}
	s.claimedRuns[runID] = claim
	s.mu.Unlock()
	return nil
}

func (s *StorageLedgerRepository) ReleaseRun(ctx context.Context, runID string, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.RLock()
	claim := s.claimedRuns[runID]
	s.mu.RUnlock()
	var err error
	if fenced, ok := s.store.(storage.FencedLeaseStore); ok && claim.Fence != 0 {
		// Only the current holder may release (repository.go contract; the
		// memory backend and the workflows ledger already enforce this). The
		// fenced release below uses the STORED claim (this instance's
		// in-memory snapshot of it), so without this gate a wrong or empty
		// holder freed a live executor's claim and got nil instead of
		// ErrClaimNotHeld (DC-2: a release any caller may perform is a claim
		// any caller may steal). The refusal writes nothing, so the claim
		// survives it and the correct holder still releases.
		if claim.Holder != holder {
			return ErrClaimNotHeld
		}
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
	// Same serialization as ClaimRun: the takeover and the closed check share
	// one critical section so a takeover racing Close can never leave a claim
	// row behind a closed store.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	claim, err := fenced.TakeoverExpiredClaimFenced(ctx, runID, holder, maxAge)
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, storage.ErrClaimHeld) {
			return ErrClaimHeld
		}
		if errors.Is(err, storage.ErrClaimNotHeld) {
			return ErrClaimNotHeld
		}
		return err
	}
	s.claimedRuns[runID] = claim
	s.mu.Unlock()
	return nil
}

func (s *StorageLedgerRepository) ClearRunClaim(ctx context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if err := s.store.ClearClaim(ctx, runID); err != nil {
		return err
	}
	// Mirror the workflows ledger: drop the in-memory holder so this instance
	// stops claiming the run (its subsequent fenced writes fail closed).
	delete(s.claimedRuns, runID)
	return nil
}

func (s *StorageLedgerRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.store.PutContent(ctx, ref, data)
}

func (s *StorageLedgerRepository) LoadContent(ctx context.Context, ref string) ([]byte, error) {
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
