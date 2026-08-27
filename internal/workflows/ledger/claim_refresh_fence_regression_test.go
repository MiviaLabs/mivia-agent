package ledger

import (
	"context"
	"errors"
	"testing"
)

// A claim holder displaced by a takeover (or whose run was resumed by another
// executor) must not regain write access. These tests pin the two halves:
//   - RefreshRunClaim is refresh-only: a heartbeat that finds no claim row
//     (the run was taken over and released by the other holder) must report
//     ErrClaimNotHeld instead of re-inserting itself.
//   - A write from a displaced holder whose claim row is gone (taken over and
//     released by the other executor) is fenced out by the ledger even though
//     the run is momentarily unclaimed.
//
// Both run over separate repository instances sharing one store (the real
// cross-process shape) so a backend difference is the defect.

func TestStorageRepository_RefreshRunClaimIsRefreshOnly(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t, "refresh")

			// No row at all: refresh must not insert.
			requireErr(t, repo.RefreshRunClaim(ctx, run, "h1"), ErrClaimNotHeld, "refresh before any claim")

			// A valid claim can be refreshed (liveness tick).
			requireErr(t, repo.ClaimRun(ctx, run, "h1"), nil, "claim by h1")
			requireErr(t, repo.RefreshRunClaim(ctx, run, "h1"), nil, "same-holder refresh")

			// A different holder cannot refresh a held row.
			requireErr(t, repo.RefreshRunClaim(ctx, run, "h2"), ErrClaimNotHeld, "refresh by non-holder while h1 holds")

			// Once the holder releases, the row is gone and refresh must report
			// lost instead of re-inserting the holder.
			requireErr(t, repo.ReleaseRun(ctx, run, "h1"), nil, "release by h1")
			requireErr(t, repo.RefreshRunClaim(ctx, run, "h1"), ErrClaimNotHeld, "refresh after release must be lost")
		})
	}
}

// TestDisplacedHolderWriteIsFencedAfterTakeoverAndRelease drives the reported
// scenario across two repository instances (executors A and B) over one store:
// A freezes holding its claim; B takes over and releases; A's next durable
// write must be refused even though the run is momentarily unclaimed.
func TestDisplacedHolderWriteIsFencedAfterTakeoverAndRelease(t *testing.T) {
	ctx := context.Background()
	for _, pair := range repoPairs() {
		t.Run(pair.name, func(t *testing.T) {
			repoA, repoB, done := pair.new(t)
			t.Cleanup(done)
			run := runID(t, "fenced")
			snap, snapshotJSON := newRun(t, run)
			requireErr(t, repoA.CreateRun(ctx, snap, snapshotJSON), nil, "create run")

			// A claims and writes while it owns the run.
			requireErr(t, repoA.ClaimRun(ctx, run, "h1"), nil, "A claims")
			requireErr(t, mutateAsHolder(ctx, repoA, run, "h1"), nil, "A writes while it holds")

			// B forcibly takes over (fence bumps) and then releases, leaving the
			// run unclaimed. A never released and still holds its captured claim.
			requireErr(t, repoB.TakeoverRunClaim(ctx, run, "h2"), nil, "B takes over")
			requireErr(t, repoB.ReleaseRun(ctx, run, "h2"), nil, "B releases")

			// A's next write is fenced out: its stale (holder, fence) no longer
			// authorizes an append against a claim row that is gone.
			err := mutateAsHolder(ctx, repoA, run, "h1")
			if err == nil {
				t.Fatal("displaced holder A write landed after takeover and release")
			}
			if !errors.Is(err, ErrClaimHeld) && !errors.Is(err, ErrClaimNotHeld) {
				t.Fatalf("displaced A write error = %v, want a fenced/held error", err)
			}

			// B can re-acquire and keep writing (the run is still live).
			requireErr(t, repoB.ClaimRun(ctx, run, "h2"), nil, "B re-claims")
			requireErr(t, mutateAsHolder(ctx, repoB, run, "h2"), nil, "B writes after re-claim")
		})
	}
}
