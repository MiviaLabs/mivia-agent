package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

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

// TakeoverRunClaim atomically replaces any existing execution claim.
func (s *StorageRepository) TakeoverRunClaim(ctx context.Context, runID, holder string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if holder == "" {
		return ErrClaimNotHeld
	}
	if err := s.store.TakeoverClaim(ctx, runID, holder); err != nil {
		return err
	}
	s.mu.Lock()
	s.claimedRuns[runID] = holder
	s.mu.Unlock()
	return nil
}

// TakeoverExpiredRunClaim replaces a claim only when its age exceeds maxAge.
func (s *StorageRepository) TakeoverExpiredRunClaim(ctx context.Context, runID, holder string, maxAge time.Duration) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if holder == "" {
		holder = s.holder
	}
	lease, ok := s.store.(storage.LeaseStore)
	if !ok {
		return ErrClaimHeld
	}
	if err := lease.TakeoverExpiredClaim(ctx, runID, holder, maxAge); err != nil {
		if errors.Is(err, storage.ErrClaimHeld) {
			return ErrClaimHeld
		}
		if errors.Is(err, storage.ErrClaimNotHeld) {
			return ErrClaimNotHeld
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
	reader, readable := s.store.(storage.ClaimReader)
	if !readable {
		return "", time.Time{}, false, nil
	}
	claim, err := reader.GetClaim(ctx, runID)
	if err != nil {
		if errors.Is(err, storage.ErrClaimNotHeld) {
			return "", time.Time{}, false, nil
		}
		return "", time.Time{}, false, err
	}
	at, err := parseClaimAcquiredAt(claim.AcquiredAt)
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("read claim %q acquired_at: %w", runID, err)
	}
	return claim.Holder, at, true, nil
}

// parseClaimAcquiredAt parses a claim's acquired_at timestamp. The SQLite
// backend stores RFC3339 with millisecond precision; legacy rows may carry
// the space-separated form.
func parseClaimAcquiredAt(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02 15:04:05", s)
}

// StoreContent persists bytes under a content-addressed reference.
func (s *StorageRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.store.PutContent(ctx, ref, data)
}

// LoadContent retrieves stored bytes. It returns ErrContentNotFound if absent.
// When the ref carries the "sha256:" prefix, the stored bytes are verified
// against the ref's embedded hex digest: a mismatch (corrupted bytes, or bytes
// stored under the wrong ref) returns an error instead of the bytes, so a bare
// ref lookup can never hand back content that does not hash to the ref. Other
// ref shapes (e.g. contentref "ref:<kind>:<hex>") are looked up verbatim
// without digest verification.
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
	if hexDigest, ok := strings.CutPrefix(ref, "sha256:"); ok {
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if !strings.EqualFold(got, hexDigest) {
			return nil, fmt.Errorf("content digest mismatch for %q: sha256(data) = %s", ref, got)
		}
	}
	return data, nil
}

// Ensure StorageRepository implements Repository at compile time.
var _ Repository = (*StorageRepository)(nil)
