package ledger

// Regression test for the ReleaseRun holder gate on the durable coordinator
// ledger (defect class DC-2: a release any caller may perform is a claim any
// caller may steal). StorageLedgerRepository.ReleaseRun previously released
// the STORED claim via ReleaseClaimFenced using this instance's in-memory
// claim snapshot without comparing claim.Holder to the passed holder, so a
// wrong or empty holder freed a live executor's claim and got nil. The
// memory backend and the workflows ledger already enforced holder-only
// release; this durable backend did not.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	sdkdf "github.com/MiviaLabs/mivia-ai-sdk/durablefence"
)

// TestStorageReleaseHolderPassesDurableFenceChecks wires the durable
// coordinator ledger's run-claim surface into the shared SDK harness and
// asserts three behaviors pinned by the original regression tests survive
// the migration: wrong-holder release is refused and the claim survives,
// stale-fence release after takeover is refused, and a released run is
// reclaimable by a third holder.
func TestStorageReleaseHolderPassesDurableFenceChecks(t *testing.T) {
	ctx := context.Background()
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			repoA, repoB, done := newReleaseHolderPair(t, backend)
			defer done()
			t.Run("wrong_holder_release_refused_claim_survives", func(t *testing.T) {
				testWrongHolderReleaseRefused(t, ctx, repoA, repoB, backend)
			})
			t.Run("stale_fence_after_takeover_release_refused", func(t *testing.T) {
				testStaleFenceReleaseRefused(t, ctx, repoA, repoB, backend)
			})
			t.Run("released_run_reclaimable_by_third_holder", func(t *testing.T) {
				testReclaimableAfterRelease(t, ctx, repoA, repoB, backend)
			})
		})
	}
}

// testWrongHolderReleaseRefused pins the behavior that a wrong holder cannot
// release, the claim survives the refusal, the correct holder still
// releases, and a third holder can claim only after release. The SDK
// harness drives the surface first; the explicit assertions below pin the
// specific behavior the original regression test covered.
func testWrongHolderReleaseRefused(t *testing.T, ctx context.Context, repoA, repoB *StorageLedgerRepository, backend string) {
	t.Helper()
	runID := "run-wrong-holder-" + backend
	if err := repoA.CreateRun(ctx, "key-"+runID, RunSnapshot{RunID: runID, Status: RunStatusCreated, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	sdkdf.RunAll(t, ctx, releaseHolderScenario(t, repoA, runID))
	if err := repoA.ClaimRun(ctx, runID, "holder-a"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := repoA.ReleaseRun(ctx, runID, "holder-b"); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("wrong-holder release: got %v, want ErrClaimNotHeld", err)
	}
	if err := repoB.ClaimRun(ctx, runID, "holder-b"); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("claim after refused release: got %v, want ErrClaimHeld", err)
	}
	if err := repoA.ReleaseRun(ctx, runID, "holder-a"); err != nil {
		t.Fatalf("release by correct holder: %v", err)
	}
	if err := repoB.ClaimRun(ctx, runID, "holder-c"); err != nil {
		t.Fatalf("third-holder claim after release: %v", err)
	}
	if err := repoB.ReleaseRun(ctx, runID, "holder-c"); err != nil {
		t.Fatalf("release holder-c: %v", err)
	}
}

// testStaleFenceReleaseRefused pins the behavior that after a takeover the
// previous holder's release is refused (the fenced release deletes zero rows
// against the advanced fence) and the new holder keeps the claim. The SDK
// harness drives the surface in a clean state first; the explicit
// assertions pin the specific stale-fence refusal after the takeover.
func testStaleFenceReleaseRefused(t *testing.T, ctx context.Context, repoA, repoB *StorageLedgerRepository, backend string) {
	t.Helper()
	runID := "run-stale-fence-" + backend
	if err := repoA.CreateRun(ctx, "key-"+runID, RunSnapshot{RunID: runID, Status: RunStatusCreated, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	sdkdf.RunAll(t, ctx, releaseHolderScenario(t, repoA, runID))
	if err := repoA.ClaimRun(ctx, runID, "holder-a"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := repoB.TakeoverExpiredRunClaim(ctx, runID, "holder-b", 0); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if err := repoA.ReleaseRun(ctx, runID, "holder-a"); !errors.Is(err, ErrClaimNotHeld) {
		t.Fatalf("stale-holder release after takeover: got %v, want ErrClaimNotHeld", err)
	}
	if err := repoB.ReleaseRun(ctx, runID, "holder-b"); err != nil {
		t.Fatalf("new-holder release after takeover: %v", err)
	}
}

// testReclaimableAfterRelease pins the behavior that after a clean release
// by the holder, a different repository instance (a new owner) can claim
// the run. The SDK harness drives the post-release surface first.
func testReclaimableAfterRelease(t *testing.T, ctx context.Context, repoA, repoB *StorageLedgerRepository, backend string) {
	t.Helper()
	runID := "run-reclaim-" + backend
	if err := repoA.CreateRun(ctx, "key-"+runID, RunSnapshot{RunID: runID, Status: RunStatusCreated, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := repoA.ClaimRun(ctx, runID, "holder-a"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := repoA.ReleaseRun(ctx, runID, "holder-a"); err != nil {
		t.Fatalf("release by holder-a: %v", err)
	}
	sdkdf.RunAll(t, ctx, releaseHolderScenario(t, repoA, runID))
	if err := repoB.ClaimRun(ctx, runID, "holder-c"); err != nil {
		t.Fatalf("third-holder claim after release: %v", err)
	}
	if err := repoB.ReleaseRun(ctx, runID, "holder-c"); err != nil {
		t.Fatalf("release holder-c: %v", err)
	}
}

// newReleaseHolderPair returns two StorageLedgerRepository instances that
// share ONE underlying store: one owned (drives lifecycle) and one borrowed
// (asserts through a different in-process snapshot).
func newReleaseHolderPair(t *testing.T, backend string) (*StorageLedgerRepository, *StorageLedgerRepository, func()) {
	t.Helper()
	if backend == "memory" {
		store := storage.NewMemory()
		return NewStorageLedgerRepository(store), NewBorrowedStorageLedgerRepository(store), func() { _ = store.Close() }
	}
	path := filepath.Join(t.TempDir(), "release-holder.db")
	store, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	return NewStorageLedgerRepository(store), NewBorrowedStorageLedgerRepository(store), func() { _ = store.Close() }
}

// releaseHolderScenario adapts StorageLedgerRepository's run-claim surface
// to the SDK Scenario shape. The run ID is captured by closure. Each Claim
// returns a fresh distinct holder identity as the SDK opaque token so the
// harness's CheckClaimRejectsWhileHeld sees a different holder on the second
// Claim than on the first.
//
// Mutate mirrors the original test's read-then-check pattern: read this
// instance's claimedRuns state via IsRunHeld + IsRunTokenFenced and return
// ErrClaimHeld for an unknown or fenced holder.
func releaseHolderScenario(t *testing.T, repo *StorageLedgerRepository, run string) sdkdf.Scenario {
	t.Helper()
	var claimN uint64
	var tkN uint64
	return sdkdf.Scenario{
		Claim: func(ctx context.Context) (string, error) {
			claimN++
			holder := claimHolder("owner", claimN)
			if err := repo.ClaimRun(ctx, run, holder); err != nil {
				return "", err
			}
			return holder, nil
		},
		Takeover: func(ctx context.Context) (string, error) {
			tkN++
			holder := claimHolder("owner-b", tkN)
			if err := repo.TakeoverExpiredRunClaim(ctx, run, holder, 0); err != nil {
				return "", err
			}
			return holder, nil
		},
		Mutate: func(ctx context.Context, holder string) error {
			held, err := repo.IsRunHeld(ctx, run)
			if err != nil {
				return err
			}
			if !held {
				return ErrClaimHeld
			}
			if fenced, err := repo.IsRunTokenFenced(ctx, run, holder); err != nil {
				return err
			} else if fenced {
				return ErrClaimHeld
			}
			return nil
		},
		Release: func(ctx context.Context, holder string) error {
			return repo.ReleaseRun(ctx, run, holder)
		},
		IsHeld: func(ctx context.Context) (bool, error) {
			return repo.IsRunHeld(ctx, run)
		},
		IsFenced: func(ctx context.Context, token string) (bool, error) {
			return repo.IsRunTokenFenced(ctx, run, token)
		},
	}
}

// claimHolder appends a monotonic counter to prefix so the SDK harness's
// CheckClaimRejectsWhileHeld sees a different holder on the second Claim
// than on the first. The counter is captured by the enclosing scenario's
// closure, so a single RunAll invocation increments it consistently.
func claimHolder(prefix string, n uint64) string {
	if n == 0 {
		return prefix
	}
	const digits = "0123456789"
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return prefix + "-" + string(buf[i:])
}
