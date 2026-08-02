package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestCatchUpAppliesTombstoneBeforeReusedKeyRunCreated is the regression guard
// for the catch-up ordering defect: catchUp used to fold runs in runID-sorted
// order, so when a deleted run's idempotency key was REUSED by a new run (the
// reclaim path DeleteRun(X) then CreateRun(K, Y)), a projection that had
// already read X's events could apply Y's run_created BEFORE X's run_deleted
// tombstone. The new run_created was then swallowed as ErrDuplicate (the old
// run still held the key in that projection's idemLookup) and the watermark
// advanced past it - the new run was PERMANENTLY LOST to that projection,
// which could then CreateRun(K) a SECOND time (double execution of keyed
// work). A fresh repository dedups correctly because it never registered the
// old run; only a projection that already held K -> X hit the loss.
//
// The scenario, deterministically, on a SQLite-backed store:
//  1. writer creates run X ("z-old-run") under key K.
//  2. stale reads X, so its projection holds K -> X and its per-run watermark
//     sits at X's tail.
//  3. writer deletes X and creates run Y ("a-new-run") under the same key K
//     (legal now: the tombstone frees K, and the writer's own projection
//     dedups correctly).
//  4. stale's next read must fold X's tombstone BEFORE Y's run_created. Y's
//     runID sorts before X's, which is exactly what made the pre-fix code fold
//     Y's created first and swallow it as a duplicate.
func TestCatchUpAppliesTombstoneBeforeReusedKeyRunCreated(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	writer := NewStorageLedgerRepository(store)
	stale := NewStorageLedgerRepository(store)

	// (1) X holds key K.
	if err := writer.CreateRun(ctx, "K", RunSnapshot{RunID: "z-old-run", Status: RunStatusCreated}); err != nil {
		t.Fatalf("writer CreateRun(X): %v", err)
	}

	// (2) stale catches up to X: its projection must know K -> X and its
	// watermark must sit at X's tail (so the next catch-up is a bounded tail).
	if got, err := stale.GetRunByIdempotencyKey(ctx, "K"); err != nil {
		t.Fatalf("stale GetRunByIdempotencyKey(K) before reclaim: %v", err)
	} else if got.RunID != "z-old-run" {
		t.Fatalf("stale key resolution before reclaim = %q, want z-old-run", got.RunID)
	}

	// (3) writer deletes X and reuses key K for Y. Y's runID sorts before X's
	// alphabetically, which the old runID-ordered fold applied first.
	if err := writer.DeleteRun(ctx, "z-old-run"); err != nil {
		t.Fatalf("writer DeleteRun(X): %v", err)
	}
	if err := writer.CreateRun(ctx, "K", RunSnapshot{RunID: "a-new-run", Status: RunStatusCreated}); err != nil {
		t.Fatalf("writer CreateRun(Y) with reused key: %v (the tombstone should have freed K)", err)
	}

	// (4) stale's next read catches up. It must apply X's tombstone THEN Y's
	// run_created, so K resolves to Y - not to the deleted X and not to
	// not-found (the bug: Y's created was swallowed as a duplicate).
	got, err := stale.GetRunByIdempotencyKey(ctx, "K")
	if err != nil {
		t.Fatalf("stale GetRunByIdempotencyKey(K) after reclaim: %v (Y's run_created was swallowed; the stale projection lost the reused key)", err)
	}
	if got.RunID != "a-new-run" {
		t.Fatalf("stale key resolution after reclaim = %q, want the new run %q", got.RunID, "a-new-run")
	}
	// The old run must be gone from the stale projection.
	if _, err := stale.GetRun(ctx, "z-old-run"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale GetRun(X) after reclaim: %v, want ErrNotFound", err)
	}

	// The stale projection must now DEDUP the key: a second CreateRun with K
	// must be refused, or the same keyed work executes twice.
	if err := stale.CreateRun(ctx, "K", RunSnapshot{RunID: "a-third-run", Status: RunStatusCreated}); err != ErrDuplicate {
		t.Fatalf("stale CreateRun(K) after reclaim: got %v, want ErrDuplicate (the stale projection did not register Y's key, so keyed work can run twice)", err)
	}

	// Sanity: a fresh repository over the same store converges on the same
	// answer, because it never registered the deleted X in the first place.
	fresh := NewStorageLedgerRepository(store)
	if got, err := fresh.GetRunByIdempotencyKey(ctx, "K"); err != nil {
		t.Fatalf("fresh GetRunByIdempotencyKey(K): %v", err)
	} else if got.RunID != "a-new-run" {
		t.Fatalf("fresh key resolution = %q, want a-new-run", got.RunID)
	}
}
