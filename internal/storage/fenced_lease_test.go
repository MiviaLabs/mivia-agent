package storage

import (
	"context"
	"errors"
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
