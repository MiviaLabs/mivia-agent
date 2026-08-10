package ledger

import (
	"context"
	"errors"
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
	if err := <-claimDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("ClaimRun racing Close = %v, want ErrClosed", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	peer := NewBorrowedStorageLedgerRepository(store)
	if err := peer.ClaimRun(ctx, "run", "peer"); err != nil {
		t.Fatalf("peer ClaimRun after close race = %v, want success", err)
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
	if err := repo.ReleaseRun(ctx, "run", "two"); err != nil {
		t.Fatalf("release uses stored fence: %v", err)
	}
	if err := repo.ReleaseRun(ctx, "run", "one"); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("missing release: %v", err)
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
