package ledgercore

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// NewHolderID generates a random per-process identifier for run execution claims.
// It uses crypto/rand to guarantee uniqueness and unpredictability across processes.
func NewHolderID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "h-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}

// ClaimsTracker manages run execution claims, fenced leases, and shutdown cleanup
// for an event-sourced ledger repository over a storage.Store.
type ClaimsTracker struct {
	holder      string
	mu          sync.RWMutex
	closed      bool
	claimedRuns map[string]storage.Claim
}

// NewClaimsTracker creates a new ClaimsTracker initialized with a holder ID.
// If holder is empty, a random holder ID is generated.
func NewClaimsTracker(holder string) *ClaimsTracker {
	if holder == "" {
		holder = NewHolderID()
	}
	return &ClaimsTracker{
		holder:      holder,
		claimedRuns: make(map[string]storage.Claim),
	}
}

// Holder returns the tracker's holder identifier.
func (c *ClaimsTracker) Holder() string {
	return c.holder
}

// IsClosed reports whether the tracker has been closed.
func (c *ClaimsTracker) IsClosed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.closed
}

// CheckOpen returns ErrClosed if the tracker is closed.
func (c *ClaimsTracker) CheckOpen() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return ErrClosed
	}
	return nil
}

// GetClaim returns the tracked storage.Claim for runID, if present.
func (c *ClaimsTracker) GetClaim(runID string) (storage.Claim, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	claim, ok := c.claimedRuns[runID]
	return claim, ok
}

// SetClaim updates the tracked storage.Claim for runID.
func (c *ClaimsTracker) SetClaim(runID string, claim storage.Claim) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claimedRuns[runID] = claim
}

// DropClaim removes runID from the tracked active claims map.
func (c *ClaimsTracker) DropClaim(runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.claimedRuns, runID)
}

// AllClaims returns a copy of all active tracked claims.
func (c *ClaimsTracker) AllClaims() map[string]storage.Claim {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]storage.Claim, len(c.claimedRuns))
	for k, v := range c.claimedRuns {
		out[k] = v
	}
	return out
}

// ClaimRun acquires an exclusive run execution claim on store.
// Serialized under lock with Close() to ensure no zombie claims are left behind.
func (c *ClaimsTracker) ClaimRun(ctx context.Context, store storage.Store, runID, holder string) error {
	if holder == "" {
		return ErrClaimNotHeld
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	var claim storage.Claim
	var err error
	if fenced, ok := store.(storage.FencedLeaseStore); ok {
		claim, err = fenced.ClaimRunFenced(ctx, runID, holder)
	} else {
		err = store.ClaimRun(ctx, runID, holder)
		claim = storage.Claim{RunID: runID, Holder: holder}
	}
	if err != nil {
		c.mu.Unlock()
		return MapStorageError(err)
	}
	c.claimedRuns[runID] = claim
	c.mu.Unlock()
	return nil
}

// RefreshRunClaim refreshes the claim's acquired_at only if already held.
func (c *ClaimsTracker) RefreshRunClaim(ctx context.Context, store storage.Store, runID, holder string) error {
	if err := c.CheckOpen(); err != nil {
		return err
	}
	if holder == "" {
		return ErrClaimNotHeld
	}
	fenced, ok := store.(storage.FencedLeaseStore)
	if !ok {
		if err := store.ClaimRun(ctx, runID, holder); err != nil {
			return MapStorageError(err)
		}
		c.mu.Lock()
		c.claimedRuns[runID] = storage.Claim{RunID: runID, Holder: holder}
		c.mu.Unlock()
		return nil
	}
	claim, err := fenced.RefreshClaimFenced(ctx, runID, holder)
	if err != nil {
		return MapStorageError(err)
	}
	c.mu.Lock()
	c.claimedRuns[runID] = claim
	c.mu.Unlock()
	return nil
}

// TakeoverRunClaim atomically replaces any existing claim on store.
func (c *ClaimsTracker) TakeoverRunClaim(ctx context.Context, store storage.Store, runID, holder string) error {
	if err := c.CheckOpen(); err != nil {
		return err
	}
	if holder == "" {
		return ErrClaimNotHeld
	}
	var claim storage.Claim
	var err error
	if fenced, ok := store.(storage.FencedLeaseStore); ok {
		claim, err = fenced.TakeoverClaimFenced(ctx, runID, holder)
	} else {
		err = store.TakeoverClaim(ctx, runID, holder)
		claim = storage.Claim{RunID: runID, Holder: holder}
	}
	if err != nil {
		return MapStorageError(err)
	}
	c.mu.Lock()
	c.claimedRuns[runID] = claim
	c.mu.Unlock()
	return nil
}

// TakeoverExpiredRunClaim replaces a claim only when its age exceeds maxAge.
func (c *ClaimsTracker) TakeoverExpiredRunClaim(ctx context.Context, store storage.Store, runID, holder string, maxAge time.Duration) error {
	if holder == "" {
		holder = c.holder
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	fenced, hasFenced := store.(storage.FencedLeaseStore)
	lease, hasLease := store.(storage.LeaseStore)
	if !hasFenced && !hasLease {
		c.mu.Unlock()
		return ErrClaimHeld
	}

	var claim storage.Claim
	var err error
	if hasFenced {
		claim, err = fenced.TakeoverExpiredClaimFenced(ctx, runID, holder, maxAge)
	} else {
		err = lease.TakeoverExpiredClaim(ctx, runID, holder, maxAge)
		claim = storage.Claim{RunID: runID, Holder: holder}
	}
	if err != nil {
		c.mu.Unlock()
		return MapStorageError(err)
	}
	c.claimedRuns[runID] = claim
	c.mu.Unlock()
	return nil
}

// ReleaseRun releases the claim on runID. Only the current holder may release it.
func (c *ClaimsTracker) ReleaseRun(ctx context.Context, store storage.Store, runID, holder string) error {
	if err := c.CheckOpen(); err != nil {
		return err
	}
	c.mu.RLock()
	claim := c.claimedRuns[runID]
	c.mu.RUnlock()

	var err error
	if fenced, ok := store.(storage.FencedLeaseStore); ok && claim.Fence != 0 {
		if claim.Holder != holder {
			return ErrClaimNotHeld
		}
		err = fenced.ReleaseClaimFenced(ctx, claim)
	} else {
		err = store.ReleaseClaim(ctx, runID, holder)
	}
	if err != nil {
		return MapStorageError(err)
	}
	c.mu.Lock()
	delete(c.claimedRuns, runID)
	c.mu.Unlock()
	return nil
}

// ClearRunClaim removes a run claim (force release).
func (c *ClaimsTracker) ClearRunClaim(ctx context.Context, store storage.Store, runID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if err := store.ClearClaim(ctx, runID); err != nil {
		return MapStorageError(err)
	}
	delete(c.claimedRuns, runID)
	return nil
}

// GetRunClaim reads the run's current execution claim without modifying it.
func (c *ClaimsTracker) GetRunClaim(ctx context.Context, store storage.Store, runID string) (holder string, acquiredAt time.Time, ok bool, err error) {
	if err := c.CheckOpen(); err != nil {
		return "", time.Time{}, false, err
	}
	reader, readable := store.(storage.ClaimReader)
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
	at, err := ParseClaimAcquiredAt(claim.AcquiredAt)
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("read claim %q acquired_at: %w", runID, err)
	}
	return claim.Holder, at, true, nil
}

// ParseClaimAcquiredAt parses a claim's acquired_at timestamp.
func ParseClaimAcquiredAt(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02 15:04:05", s)
}

// IsRunHeld reports whether runID currently has an active claim.
func (c *ClaimsTracker) IsRunHeld(ctx context.Context, store storage.Store, runID string) (bool, error) {
	if err := c.CheckOpen(); err != nil {
		return false, err
	}
	reader, ok := store.(interface {
		IsRunHeld(context.Context, string) (bool, error)
	})
	if !ok {
		return false, nil
	}
	return reader.IsRunHeld(ctx, runID)
}

// IsRunTokenFenced reports whether token has been fenced out of runID.
func (c *ClaimsTracker) IsRunTokenFenced(ctx context.Context, store storage.Store, runID, token string) (bool, error) {
	if err := c.CheckOpen(); err != nil {
		return false, err
	}
	reader, ok := store.(interface {
		IsRunTokenFenced(context.Context, string, string) (bool, error)
	})
	if !ok {
		return false, nil
	}
	return reader.IsRunTokenFenced(ctx, runID, token)
}

// Close marks the tracker as closed and releases all active claims held by this instance.
// It never closes the underlying store.
func (c *ClaimsTracker) Close(ctx context.Context, store storage.Store) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	claims := make(map[string]storage.Claim, len(c.claimedRuns))
	for runID, claim := range c.claimedRuns {
		claims[runID] = claim
	}
	c.claimedRuns = make(map[string]storage.Claim)
	c.mu.Unlock()

	relCtx := ctx
	var cancel context.CancelFunc
	if relCtx == nil {
		relCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}

	for runID, claim := range claims {
		if fenced, ok := store.(storage.FencedLeaseStore); ok && claim.Fence != 0 {
			_ = fenced.ReleaseClaimFenced(relCtx, claim)
		} else {
			_ = store.ReleaseClaim(relCtx, runID, claim.Holder)
		}
	}
	return nil
}
