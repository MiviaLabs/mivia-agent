package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestSQLiteCrossProcessClaim verifies SQLite UPSERT claim semantics across
// multiple repository instances sharing one store. This is a regression guard:
// it must fail if SQLite.ClaimRun's ON CONFLICT ... WHERE clause is removed or
// broken, because repo2.ClaimRun would silently succeed instead of returning
// ErrClaimHeld.
func TestSQLiteCrossProcessClaim(t *testing.T) {
	ctx := context.Background()

	// 1. Open a SQLite database.
	dbPath := filepath.Join(t.TempDir(), "fence.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Create TWO separate repository instances over the same store handle,
	// simulating two processes sharing one workspace.
	repo1 := NewStorageLedgerRepository(store)
	repo2 := NewStorageLedgerRepository(store)

	runID := "run-test-fence"

	// repo1 claims the run.
	if err := repo1.ClaimRun(ctx, runID, "holder-1"); err != nil {
		t.Fatalf("repo1 claim: %v", err)
	}

	// repo2 trying the same run ID must be refused (different holder).
	if err := repo2.ClaimRun(ctx, runID, "holder-2"); !errors.Is(err, ErrClaimHeld) {
		if err == nil {
			t.Fatal("MUTATION FAIL: repo2 claimed run held by repo1 (UPSERT WHERE clause may be missing)")
		}
		t.Fatalf("repo2 claim: want ErrClaimHeld, got %v", err)
	}

	// repo1 refreshes its own claim (same holder - succeeds).
	if err := repo1.ClaimRun(ctx, runID, "holder-1"); err != nil {
		t.Fatalf("repo1 refresh: %v", err)
	}

	// repo1 releases.
	if err := repo1.ReleaseRun(ctx, runID, "holder-1"); err != nil {
		t.Fatalf("repo1 release: %v", err)
	}

	// repo2 claims after release - must succeed now.
	if err := repo2.ClaimRun(ctx, runID, "holder-2"); err != nil {
		t.Fatalf("repo2 claim after release: %v", err)
	}

	// Release repo2's claim so repo1.Close() doesn't try to release it too.
	if err := repo2.ReleaseRun(ctx, runID, "holder-2"); err != nil {
		t.Fatalf("repo2 release: %v", err)
	}

	// repo1.Close() releases its tracked claims and closes the shared store.
	if err := repo1.Close(); err != nil {
		t.Fatalf("repo1 close: %v", err)
	}

	// Open a new store to the same SQLite file and create a third repo.
	// The claims from repo1 were released to the database before close, so
	// repo3 should be able to claim the run.
	store2, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	repo3 := NewStorageLedgerRepository(store2)
	t.Cleanup(func() { _ = repo3.Close() })

	if err := repo3.ClaimRun(ctx, runID, "holder-3"); err != nil {
		t.Fatalf("repo3 claim after repo1 close: %v", err)
	}
}
