package ledger

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

type unfencedClaimStore struct{ storage.Store }

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
