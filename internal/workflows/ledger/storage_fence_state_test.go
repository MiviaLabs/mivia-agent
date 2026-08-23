package ledger

import (
	"context"
	"testing"
)

// This file pins the read-only IsRunHeld / IsRunTokenFenced probes on the
// workflows StorageRepository over both backends. The probes observe durable
// claim state without acquiring, refreshing, or releasing a claim.

// TestStorageRepository_IsRunHeld pins the liveness probe on every backend.
func TestStorageRepository_IsRunHeld(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			// Fresh run.
			held, err := repo.IsRunHeld(ctx, run)
			if err != nil || held {
				t.Fatalf("fresh IsRunHeld = held %v err %v, want false nil", held, err)
			}
			// After claim.
			requireErr(t, repo.ClaimRun(ctx, run, "holder-a"), nil, "claim by holder-a")
			held, err = repo.IsRunHeld(ctx, run)
			if err != nil || !held {
				t.Fatalf("claimed IsRunHeld = held %v err %v, want true nil", held, err)
			}
			// After release by the holder.
			requireErr(t, repo.ReleaseRun(ctx, run, "holder-a"), nil, "release by holder-a")
			held, err = repo.IsRunHeld(ctx, run)
			if err != nil || held {
				t.Fatalf("released IsRunHeld = held %v err %v, want false nil", held, err)
			}
		})
	}
}

// TestStorageRepository_IsRunTokenFenced pins the fence-history probe on every
// backend across the takeover matrix.
func TestStorageRepository_IsRunTokenFenced(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t, "fence")
			// Unknown token: never a holder.
			fenced, err := repo.IsRunTokenFenced(ctx, run, "never-held")
			if err != nil || fenced {
				t.Fatalf("unknown IsRunTokenFenced = fenced %v err %v, want false nil", fenced, err)
			}
			// holder-a claims; they are the current owner and read false.
			requireErr(t, repo.ClaimRun(ctx, run, "holder-a"), nil, "claim by holder-a")
			fenced, err = repo.IsRunTokenFenced(ctx, run, "holder-a")
			if err != nil || fenced {
				t.Fatalf("current owner IsRunTokenFenced = fenced %v err %v, want false nil", fenced, err)
			}
			// holder-b takes over; holder-a is now fenced out.
			requireErr(t, repo.TakeoverRunClaim(ctx, run, "holder-b"), nil, "takeover by holder-b")
			fenced, err = repo.IsRunTokenFenced(ctx, run, "holder-a")
			if err != nil || !fenced {
				t.Fatalf("prior holder IsRunTokenFenced = fenced %v err %v, want true nil", fenced, err)
			}
			// holder-b (the current owner) still reads false.
			fenced, err = repo.IsRunTokenFenced(ctx, run, "holder-b")
			if err != nil || fenced {
				t.Fatalf("current owner IsRunTokenFenced = fenced %v err %v, want false nil", fenced, err)
			}
			// holder-b releases. The fenced history is durable: holder-a
			// is still fenced, even though the run has no current claim.
			requireErr(t, repo.ReleaseRun(ctx, run, "holder-b"), nil, "release by holder-b")
			fenced, err = repo.IsRunTokenFenced(ctx, run, "holder-a")
			if err != nil || !fenced {
				t.Fatalf("durable IsRunTokenFenced = fenced %v err %v, want true nil", fenced, err)
			}
			held, err := repo.IsRunHeld(ctx, run)
			if err != nil || held {
				t.Fatalf("after-release IsRunHeld = held %v err %v, want false nil", held, err)
			}
		})
	}
}

// TestStorageRepository_IsRunHeldClosedErrors pins the closed-repo error branch
// for both probes on the MemoryRepository path.
func TestStorageRepository_IsRunHeldClosedErrors(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepo(t)
	repo.Close()

	if _, err := repo.IsRunHeld(ctx, "wfr-x"); err == nil {
		t.Fatal("IsRunHeld on closed repo must error")
	}
	if _, err := repo.IsRunTokenFenced(ctx, "wfr-x", "tok"); err == nil {
		t.Fatal("IsRunTokenFenced on closed repo must error")
	}
}

// TestStorageRepository_MemoryRepositoryFenceHistory pins the fence-history
// probe on the in-memory backend specifically. The Memory store records fenced
// tokens on takeovers the same way the SQLite store does, so a re-issued
// claim by a previously fenced holder still reads true after a fresh takeover.
func TestStorageRepository_MemoryRepositoryFenceHistory(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepo(t)

	requireErr(t, repo.ClaimRun(ctx, "wfr-mem-1", "h-a"), nil, "claim h-a")
	requireErr(t, repo.TakeoverRunClaim(ctx, "wfr-mem-1", "h-b"), nil, "takeover h-b")
	requireErr(t, repo.TakeoverRunClaim(ctx, "wfr-mem-1", "h-c"), nil, "takeover h-c")

	// Both h-a and h-b have been fenced out by a later takeover.
	fencedA, err := repo.IsRunTokenFenced(ctx, "wfr-mem-1", "h-a")
	if err != nil || !fencedA {
		t.Fatalf("h-a fence: fenced %v err %v, want true nil", fencedA, err)
	}
	fencedB, err := repo.IsRunTokenFenced(ctx, "wfr-mem-1", "h-b")
	if err != nil || !fencedB {
		t.Fatalf("h-b fence: fenced %v err %v, want true nil", fencedB, err)
	}
	// h-c is the current owner and is therefore never fenced out.
	fencedC, err := repo.IsRunTokenFenced(ctx, "wfr-mem-1", "h-c")
	if err != nil || fencedC {
		t.Fatalf("h-c fence: fenced %v err %v, want false nil", fencedC, err)
	}
	// h-c releases; the durable fence history outlives the current owner.
	requireErr(t, repo.ReleaseRun(ctx, "wfr-mem-1", "h-c"), nil, "release h-c")
	fencedA, err = repo.IsRunTokenFenced(ctx, "wfr-mem-1", "h-a")
	if err != nil || !fencedA {
		t.Fatalf("h-a fence after release: fenced %v err %v, want true nil", fencedA, err)
	}
}
