package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestFencedLeaseTakeoverBlocksFreshAndRefusesStaleOwner(t *testing.T) {
	ctx := context.Background()
	stores := map[string]Store{
		"memory": NewMemory(),
	}
	sqlite, err := OpenSQLite(filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatal(err)
	}
	stores["sqlite"] = sqlite
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { _ = store.Close() })
			lease := store.(FencedLeaseStore)
			old, err := lease.ClaimRunFenced(ctx, "run-1", "old")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lease.TakeoverExpiredClaimFenced(ctx, "run-1", "new", time.Hour); !errors.Is(err, ErrClaimHeld) {
				t.Fatalf("fresh takeover error = %v, want ErrClaimHeld", err)
			}
			current, err := lease.TakeoverExpiredClaimFenced(ctx, "run-1", "new", 0)
			if err != nil {
				t.Fatal(err)
			}
			if current.Fence <= old.Fence {
				t.Fatalf("new fence = %d, old fence = %d", current.Fence, old.Fence)
			}
			event := Event{ID: "event-1", RunID: "run-1", Sequence: 1, Kind: "test", Payload: []byte("x")}
			if err := lease.AppendClaimedFenced(ctx, event, old); !errors.Is(err, ErrClaimHeld) {
				t.Fatalf("stale append error = %v, want ErrClaimHeld", err)
			}
			if err := lease.AppendClaimedFenced(ctx, event, current); err != nil {
				t.Fatalf("current append: %v", err)
			}
		})
	}
}

func TestFencedLeaseRejectsInvalidAndStaleOperations(t *testing.T) {
	ctx := context.Background()
	for name, store := range map[string]FencedLeaseStore{
		"memory": NewMemory(),
		"sqlite": mustOpenFencedSQLite(t),
	} {
		t.Run(name, func(t *testing.T) {
			if closer, ok := store.(interface{ Close() error }); ok {
				t.Cleanup(func() { _ = closer.Close() })
			}
			if _, err := store.ClaimRunFenced(ctx, "run", ""); !errors.Is(err, ErrClaimNotHeld) {
				t.Fatalf("empty holder claim: %v", err)
			}
			if _, err := store.TakeoverExpiredClaimFenced(ctx, "missing", "new", 0); !errors.Is(err, ErrClaimNotHeld) {
				t.Fatalf("missing takeover: %v", err)
			}
			claim, err := store.ClaimRunFenced(ctx, "run", "owner")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.TakeoverExpiredClaimFenced(ctx, "run", "new", time.Hour); !errors.Is(err, ErrClaimHeld) {
				t.Fatalf("fresh takeover: %v", err)
			}
			if err := store.AppendClaimedFenced(ctx, Event{ID: "empty", RunID: "run", Sequence: 1}, claim); err == nil {
				t.Fatal("empty event append succeeded")
			}
			event := Event{ID: "event", RunID: "run", Sequence: 1, Kind: "test", Payload: []byte("x")}
			if err := store.AppendClaimedFenced(ctx, event, Claim{RunID: "run", Holder: "owner", Fence: claim.Fence + 1}); !errors.Is(err, ErrClaimHeld) {
				t.Fatalf("stale append: %v", err)
			}
			if err := store.AppendClaimedFenced(ctx, event, claim); err != nil {
				t.Fatal(err)
			}
			if err := store.AppendClaimedFenced(ctx, event, claim); !errors.Is(err, ErrDuplicate) {
				t.Fatalf("duplicate append: %v", err)
			}
			if err := store.ReleaseClaimFenced(ctx, Claim{RunID: "run", Holder: "owner", Fence: claim.Fence + 1}); !errors.Is(err, ErrClaimNotHeld) {
				t.Fatalf("stale release: %v", err)
			}
		})
	}
}

func TestMemoryFencedLeaseHandlesInvalidTimestampAndFenceWrap(t *testing.T) {
	store := NewMemory()
	store.claims["bad"] = Claim{RunID: "bad", Holder: "old", AcquiredAt: "invalid", Fence: 1}
	if _, err := store.TakeoverExpiredClaimFenced(context.Background(), "bad", "new", 0); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("invalid timestamp takeover: %v", err)
	}
	store.claims["wrap"] = Claim{RunID: "wrap", Holder: "old", AcquiredAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), Fence: math.MaxUint64}
	claim, err := store.TakeoverExpiredClaimFenced(context.Background(), "wrap", "new", 0)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Fence != 1 {
		t.Fatalf("wrapped fence = %d, want 1", claim.Fence)
	}
}

func TestOpenSQLiteMigratesLegacyClaimFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE run_claims (run_id TEXT PRIMARY KEY, holder TEXT NOT NULL, acquired_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	claim, err := store.ClaimRunFenced(context.Background(), "run", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if claim.Fence != 1 {
		t.Fatalf("legacy fence = %d, want 1", claim.Fence)
	}
}

func mustOpenFencedSQLite(t *testing.T) *SQLite {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
