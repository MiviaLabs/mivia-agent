package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

type unfencedClaimStore struct{ storage.Store }

type blockedFencedClaimStore struct {
	storage.Store
	entered chan struct{}
	release chan struct{}
}

func (s *blockedFencedClaimStore) ClaimRunFenced(ctx context.Context, runID, holder string) (storage.Claim, error) {
	select {
	case <-s.entered:
		return s.Store.(storage.FencedLeaseStore).ClaimRunFenced(ctx, runID, holder)
	default:
		close(s.entered)
		<-s.release
	}
	return s.Store.(storage.FencedLeaseStore).ClaimRunFenced(ctx, runID, holder)
}

func (s *blockedFencedClaimStore) TakeoverExpiredClaimFenced(ctx context.Context, runID, holder string, maxAge time.Duration) (storage.Claim, error) {
	return s.Store.(storage.FencedLeaseStore).TakeoverExpiredClaimFenced(ctx, runID, holder, maxAge)
}

func (s *blockedFencedClaimStore) AppendClaimedFenced(ctx context.Context, event storage.Event, claim storage.Claim) error {
	return s.Store.(storage.FencedLeaseStore).AppendClaimedFenced(ctx, event, claim)
}

func (s *blockedFencedClaimStore) ReleaseClaimFenced(ctx context.Context, claim storage.Claim) error {
	return s.Store.(storage.FencedLeaseStore).ReleaseClaimFenced(ctx, claim)
}

func TestStorageClaimsFallbackStore(t *testing.T) {
	ctx := context.Background()
	store := &unfencedClaimStore{Store: storage.NewMemory()}
	repo := NewStorageLedgerRepository(store)
	if err := repo.ClaimRun(ctx, "run", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReleaseRun(ctx, "run", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := repo.TakeoverExpiredRunClaim(ctx, "run", "owner", 0); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("unsupported lease error = %v", err)
	}
	if err := repo.ClaimRun(ctx, "close", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStorageClaimsRejectClosedRepository(t *testing.T) {
	ctx := context.Background()
	repo := NewStorageLedgerRepository(storage.NewMemory())
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, "run", "owner"); !errors.Is(err, ErrClosed) {
		t.Fatalf("ClaimRun after Close = %v, want ErrClosed", err)
	}
}

// TestStorageClaimsRejectClaimThatRacesClose pins the ClaimRun-vs-Close
// invariant: after the race, the repository is closed and NO claim survives it
// (a peer claim on the same run succeeds). Either legal outcome is accepted -
// ClaimRun returning ErrClosed (Close won the serialized section) or nil with
// the claim collected and released by Close's snapshot (ClaimRun won). The
// outcome must never be a leaked claim row.
func TestStorageClaimsRejectClaimThatRacesClose(t *testing.T) {
	ctx := context.Background()
	store := &blockedFencedClaimStore{
		Store:   storage.NewMemory(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	repo := NewStorageLedgerRepository(store)
	claimDone := make(chan error, 1)
	go func() { claimDone <- repo.ClaimRun(ctx, "run", "owner") }()
	<-store.entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- repo.Close() }()
	close(store.release)
	if err := <-claimDone; err != nil && !errors.Is(err, ErrClosed) {
		t.Fatalf("ClaimRun racing Close = %v, want nil or ErrClosed", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	peer := NewBorrowedStorageLedgerRepository(store)
	if err := peer.ClaimRun(ctx, "run", "peer"); err != nil {
		t.Fatalf("peer ClaimRun after close race = %v, want success (no claim may survive Close)", err)
	}
}

// TestStorageClaimsCloseRaceLeaksNoClaimOnOwnedStore proves the same invariant
// on an OWNED SQLite store, where the post-close compensating release could
// fail against the closed database and leave a durable claim row. The fix
// serializes the fenced insert with the closed check, so either the claim is
// registered before Close's snapshot (and Close releases it while the store is
// still open) or ClaimRun is refused before touching the store.
func TestStorageClaimsCloseRaceLeaksNoClaimOnOwnedStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	inner, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	blocked := &blockedFencedClaimStore{
		Store:   inner,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	repo := NewStorageLedgerRepository(blocked)
	claimDone := make(chan error, 1)
	go func() { claimDone <- repo.ClaimRun(ctx, "run", "owner") }()
	<-blocked.entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- repo.Close() }()
	close(blocked.release)
	if err := <-claimDone; err != nil && !errors.Is(err, ErrClosed) {
		t.Fatalf("ClaimRun racing Close = %v, want nil or ErrClosed", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	// Reopen the same file: no claim row may survive the shutdown race.
	reopened, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	peer := NewBorrowedStorageLedgerRepository(reopened)
	if err := peer.ClaimRun(ctx, "run", "peer"); err != nil {
		t.Fatalf("peer ClaimRun after owned-store close race = %v, want success (the claim row must not survive)", err)
	}
}

func TestStorageClaimsFallbackStoreDeletesRun(t *testing.T) {
	ctx := context.Background()
	store := &unfencedClaimStore{Store: storage.NewMemory()}
	repo := NewStorageLedgerRepository(store)
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run", Status: RunStatusCreated}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRun(ctx, "run"); err != nil {
		t.Fatalf("DeleteRun through Store adapter: %v", err)
	}
}

func TestStorageClaimsTranslateFencedErrors(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)
	if err := repo.ClaimRun(ctx, "run", "one"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, "run", "two"); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("claim conflict: %v", err)
	}
	// Only the holder may release (repository.go contract; the memory backend
	// and the workflows ledger agree): the non-holder is refused, the claim
	// stays held, and the holder then releases it.
	if err := repo.ReleaseRun(ctx, "run", "two"); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("release by non-holder: got %v, want ErrClaimNotHeld", err)
	}
	if err := repo.ClaimRun(ctx, "run", "three"); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("claim after refused release: got %v, want ErrClaimHeld", err)
	}
	if err := repo.ReleaseRun(ctx, "run", "one"); err != nil {
		t.Fatalf("release by holder: %v", err)
	}
	if err := repo.TakeoverExpiredRunClaim(ctx, "missing", "two", time.Minute); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("missing takeover: %v", err)
	}
	if err := repo.ClaimRun(ctx, "fresh", "one"); err != nil {
		t.Fatal(err)
	}
	if err := repo.TakeoverExpiredRunClaim(ctx, "fresh", "two", time.Hour); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("fresh takeover: %v", err)
	}
}
