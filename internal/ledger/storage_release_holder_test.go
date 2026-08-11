package ledger

// Regression test for the ReleaseRun holder gate on the durable (SQLite-backed)
// coordinator ledger — defect class DC-2 (claim, lease, and fence; the
// durablefence.CheckReleaseIsHolderOnly doctrine: a release any caller may
// perform is a claim any caller may steal).
//
// StorageLedgerRepository.ReleaseRun released the STORED claim via
// ReleaseClaimFenced using this instance's in-memory claim snapshot without
// comparing claim.Holder to the passed holder, so a wrong or empty holder freed
// a live executor's claim and got nil. The memory backend
// (TestMemoryBackendClaimIsExclusive) and the workflows ledger both enforce
// holder-only release; the durable coordinator ledger did not, so a non-holder
// could release a claim and let a third process claim the run mid-execution,
// fencing the live owner's subsequent writes with ErrClaimHeld.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/durablefence"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestStorageReleaseRunRequiresMatchingHolder pins the repository.go contract
// ("Only the current holder may release. Returns ErrClaimNotHeld if the caller
// does not hold the claim") on the durable backend, over ONE SQLite store with
// two repository instances. Each scenario runs as its own subtest; the bodies
// live in the testRelease* helpers below so no function exceeds the go-structure
// soft limit of 80 lines.
func TestStorageReleaseRunRequiresMatchingHolder(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "release-holder.db"))
	if err != nil {
		t.Fatal(err)
	}
	repoA := NewStorageLedgerRepository(store)
	repoB := NewBorrowedStorageLedgerRepository(store)
	t.Cleanup(func() { _ = repoA.Close() })
	t.Cleanup(func() { _ = repoB.Close() })

	t.Run("wrong holder refused and the claim survives", func(t *testing.T) {
		testReleaseWrongHolder(t, ctx, repoA, repoB)
	})
	t.Run("empty holder refused and the claim survives", func(t *testing.T) {
		testReleaseEmptyHolder(t, ctx, repoA, repoB)
	})
	t.Run("stale fence after takeover refused", func(t *testing.T) {
		testReleaseStaleFence(t, ctx, repoA, repoB)
	})
	t.Run("unfenced boundary uses the store holder gate", func(t *testing.T) {
		testReleaseUnfencedBoundary(t, ctx, repoA, repoB)
	})
}

// createReleaseHolderRun creates a run for one holder-gate scenario.
func createReleaseHolderRun(t *testing.T, ctx context.Context, repo *StorageLedgerRepository, runID string) {
	t.Helper()
	if err := repo.CreateRun(ctx, "key-"+runID, RunSnapshot{RunID: runID, Status: RunStatusCreated, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun(%s): %v", runID, err)
	}
}

// testReleaseWrongHolder: a wrong holder must not release, and the claim must
// survive the refusal — a second instance cannot claim it, the correct holder
// still releases it, and only then may a third holder claim.
func testReleaseWrongHolder(t *testing.T, ctx context.Context, repoA, repoB *StorageLedgerRepository) {
	t.Helper()
	createReleaseHolderRun(t, ctx, repoA, "run-wrong-holder")
	if err := repoA.ClaimRun(ctx, "run-wrong-holder", "holder-a"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := repoA.ReleaseRun(ctx, "run-wrong-holder", "holder-b"); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("ReleaseRun by wrong holder: got %v, want ErrClaimNotHeld", err)
	}
	// The claim is still held after the refusal: a second instance cannot
	// claim it, and the correct holder still releases it.
	if err := repoB.ClaimRun(ctx, "run-wrong-holder", "holder-b"); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("claim after refused release: got %v, want ErrClaimHeld", err)
	}
	if err := repoA.ReleaseRun(ctx, "run-wrong-holder", "holder-a"); err != nil {
		t.Fatalf("ReleaseRun by correct holder: %v", err)
	}
	// A third holder can claim only after the release.
	if err := repoB.ClaimRun(ctx, "run-wrong-holder", "holder-c"); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	if err := repoB.ReleaseRun(ctx, "run-wrong-holder", "holder-c"); err != nil {
		t.Fatalf("release holder-c: %v", err)
	}
}

// testReleaseEmptyHolder: an empty holder must not release on the fenced path,
// the claim survives the refusal, and the correct holder still releases it.
func testReleaseEmptyHolder(t *testing.T, ctx context.Context, repoA, repoB *StorageLedgerRepository) {
	t.Helper()
	createReleaseHolderRun(t, ctx, repoA, "run-empty-holder")
	if err := repoA.ClaimRun(ctx, "run-empty-holder", "holder-a"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := repoA.ReleaseRun(ctx, "run-empty-holder", ""); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("ReleaseRun by empty holder: got %v, want ErrClaimNotHeld", err)
	}
	if err := repoB.ClaimRun(ctx, "run-empty-holder", "holder-b"); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("claim after empty-holder refusal: got %v, want ErrClaimHeld", err)
	}
	if err := repoA.ReleaseRun(ctx, "run-empty-holder", "holder-a"); err != nil {
		t.Fatalf("release by correct holder: %v", err)
	}
}

// testReleaseStaleFence: after a takeover the previous holder's release is
// refused (ReleaseClaimFenced deletes zero rows against the advanced fence)
// and the new holder keeps the claim.
func testReleaseStaleFence(t *testing.T, ctx context.Context, repoA, repoB *StorageLedgerRepository) {
	t.Helper()
	createReleaseHolderRun(t, ctx, repoA, "run-stale-fence")
	if err := repoA.ClaimRun(ctx, "run-stale-fence", "holder-a"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := repoB.TakeoverExpiredRunClaim(ctx, "run-stale-fence", "holder-b", 0); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if err := repoA.ReleaseRun(ctx, "run-stale-fence", "holder-a"); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("stale-holder release after takeover: got %v, want ErrClaimNotHeld", err)
	}
	if err := repoB.ReleaseRun(ctx, "run-stale-fence", "holder-b"); err != nil {
		t.Fatalf("new-holder release after takeover: %v", err)
	}
}

// testReleaseUnfencedBoundary: an instance with NO in-memory claim
// (claim.Fence == 0) falls through to the unchanged unfenced store path, where
// the store-level holder gate applies: the correct holder string succeeds, a
// wrong one is refused.
func testReleaseUnfencedBoundary(t *testing.T, ctx context.Context, repoA, repoB *StorageLedgerRepository) {
	t.Helper()
	createReleaseHolderRun(t, ctx, repoA, "run-unfenced")
	if err := repoB.ClaimRun(ctx, "run-unfenced", "holder-b"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := repoA.ReleaseRun(ctx, "run-unfenced", "holder-b"); err != nil {
		t.Fatalf("unfenced release with matching holder: %v", err)
	}
	createReleaseHolderRun(t, ctx, repoA, "run-unfenced-wrong")
	if err := repoB.ClaimRun(ctx, "run-unfenced-wrong", "holder-b"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := repoA.ReleaseRun(ctx, "run-unfenced-wrong", "wrong"); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("unfenced release with wrong holder: got %v, want ErrClaimNotHeld", err)
	}
	if err := repoB.ReleaseRun(ctx, "run-unfenced-wrong", "holder-b"); err != nil {
		t.Fatalf("cleanup release: %v", err)
	}
}

// TestStorageLedgerRunClaimPassesDurableFenceChecks runs the shared
// durable-ownership harness (durablefence package) over the storage-ledger run
// claim, extending the claim-exclusivity surface pinned by INV-DUR-2. On the
// unfixed code CheckReleaseIsHolderOnly fails (a non-holder's release returns
// nil); after the fix all four checks pass.
func TestStorageLedgerRunClaimPassesDurableFenceChecks(t *testing.T) {
	durablefence.Run(t, "storage-ledger", func(tb testing.TB) durablefence.Scenario {
		t, ok := tb.(*testing.T)
		if !ok {
			tb.Fatalf("storage-ledger scenario needs *testing.T, got %T", tb)
		}
		store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "storage-ledger-fence.db"))
		if err != nil {
			t.Fatal(err)
		}
		repo := NewStorageLedgerRepository(store)
		t.Cleanup(func() { _ = repo.Close() })

		ctx := context.Background()
		runID := "run-fence"
		taskID := "t1"
		if err := repo.CreateRun(ctx, "key-"+runID, RunSnapshot{RunID: runID, Status: RunStatusCreated, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := repo.CreateTask(ctx, TaskSnapshot{
			RunID: runID, TaskID: taskID, HandlerName: "h", Input: []byte(`{}`),
			Scope: "test", Status: string(TaskStatusQueued),
		}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return durablefence.Scenario{
			Name: "storage ledger run claim",
			Claim: func(ctx context.Context, holder string) error {
				return repo.ClaimRun(ctx, runID, holder)
			},
			Takeover: func(ctx context.Context, holder string) error {
				return repo.TakeoverExpiredRunClaim(ctx, runID, holder, 0)
			},
			Mutate: func(ctx context.Context, holder string) error {
				// The storage ledger fences mutations on THIS repository
				// instance's claim state (appendStoreEvent uses
				// s.claimedRuns, not a per-call holder). Route the harness's
				// holder to that state: a mutation attributed to a holder the
				// instance does not currently hold is refused exactly as a
				// stale process's write is refused by the store fence after a
				// takeover.
				repo.mu.RLock()
				claim := repo.claimedRuns[runID]
				repo.mu.RUnlock()
				if claim.Holder != holder {
					return ErrClaimHeld
				}
				current, err := repo.GetTask(ctx, runID, taskID)
				if err != nil {
					return err
				}
				next, ok := nextTaskStatus(current.Status)
				if !ok {
					return ErrInvalidTransition
				}
				return repo.CompareAndSetTaskStatus(ctx, runID, taskID, current.Version, next)
			},
			Release: func(ctx context.Context, holder string) error {
				return repo.ReleaseRun(ctx, runID, holder)
			},
			IsHeld: durablefence.ErrIs(ErrClaimHeld),
		}
	})
}

// nextTaskStatus returns the next legal NON-TERMINAL task status after from,
// cycling queued -> running -> awaiting_input -> running (transition.go). A
// terminal target would make the stale-owner Mutate fail on
// ErrInvalidTransition before the claim fence is ever consulted, passing the
// fence check for the wrong reason.
func nextTaskStatus(from string) (string, bool) {
	switch from {
	case string(TaskStatusQueued):
		return string(TaskStatusRunning), true
	case string(TaskStatusRunning):
		return string(TaskStatusAwaitingInput), true
	case string(TaskStatusAwaitingInput):
		return string(TaskStatusRunning), true
	default:
		return "", false
	}
}
