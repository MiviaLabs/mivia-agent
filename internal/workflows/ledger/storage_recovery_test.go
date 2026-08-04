package ledger

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// 15. Recover
// ---------------------------------------------------------------------------

// recoverFixture admits an interrupted and a completed run on repoA, both
// claimed by repoA, for the Recover classification tests.
func recoverFixture(t *testing.T, ctx context.Context, repoA *StorageRepository) (interrupted, completed string) {
	t.Helper()
	interrupted = runID(t, "interrupted")
	completed = runID(t, "completed")

	// Interrupted run: created, moved to running, claim held by repoA.
	snapI, jsonI := newRun(t, interrupted)
	requireErr(t, repoA.CreateRun(ctx, snapI, jsonI), nil, "create interrupted run")
	requireErr(t, repoA.CompareAndSetRunStatus(ctx, interrupted, 1, RunStatusRunning, nil),
		nil, "interrupted -> running")
	requireErr(t, repoA.ClaimRun(ctx, interrupted, "repoA"), nil, "claim interrupted run")

	// Completed run: created, ran, succeeded; claim also held by repoA.
	snapC, jsonC := newRun(t, completed)
	requireErr(t, repoA.CreateRun(ctx, snapC, jsonC), nil, "create completed run")
	requireErr(t, repoA.CompareAndSetRunStatus(ctx, completed, 1, RunStatusRunning, nil),
		nil, "completed -> running")
	fin := fixedClock
	requireErr(t, repoA.CompareAndSetRunStatus(ctx, completed, 2, RunStatusSucceeded, &fin),
		nil, "completed -> succeeded")
	requireErr(t, repoA.ClaimRun(ctx, completed, "repoA"), nil, "claim completed run")
	return interrupted, completed
}

// TestStorageRepository_RecoverClassifiesAndClearsTerminalClaims covers the
// classification side of Recover: the completed run is reported terminal (not
// interrupted) and its stale claim is cleared, so a fresh executor may claim
// it.
func TestStorageRepository_RecoverClassifiesAndClearsTerminalClaims(t *testing.T) {
	ctx := context.Background()
	for _, p := range repoPairs() {
		t.Run(p.name, func(t *testing.T) {
			repoA, repoB, done := p.new(t)
			defer done()

			_, completed := recoverFixture(t, ctx, repoA)

			// repoB recovers: classifies both runs.
			recovered, err := repoB.Recover(ctx)
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}
			if len(recovered) != 2 {
				t.Fatalf("Recover reported %d runs, want 2", len(recovered))
			}
			byID := make(map[string]RecoveredRun, len(recovered))
			for _, r := range recovered {
				byID[r.RunID] = r
			}

			cr, ok := byID[completed]
			if !ok {
				t.Fatalf("Recover missing completed run %q", completed)
			}
			if cr.WasInterrupted {
				t.Fatalf("completed run: WasInterrupted = true, want false")
			}
			if cr.Status != RunStatusSucceeded {
				t.Fatalf("completed run Status = %q, want succeeded", cr.Status)
			}

			// The claim on the TERMINAL run was cleared by Recover.
			requireErr(t, repoB.ClaimRun(ctx, completed, "fresh-holder"),
				nil, "claim completed run after Recover")
		})
	}
}

// TestStorageRepository_RecoverKeepsInterruptedClaim covers the interrupted
// side of Recover: the running run is reported interrupted with its CreatedAt,
// its claim survives Recover, and Recover mutates no run status.
func TestStorageRepository_RecoverKeepsInterruptedClaim(t *testing.T) {
	ctx := context.Background()
	for _, p := range repoPairs() {
		t.Run(p.name, func(t *testing.T) {
			repoA, repoB, done := p.new(t)
			defer done()

			interrupted, completed := recoverFixture(t, ctx, repoA)

			recovered, err := repoB.Recover(ctx)
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}
			byID := make(map[string]RecoveredRun, len(recovered))
			for _, r := range recovered {
				byID[r.RunID] = r
			}

			ir, ok := byID[interrupted]
			if !ok {
				t.Fatalf("Recover missing interrupted run %q", interrupted)
			}
			if !ir.WasInterrupted {
				t.Fatalf("interrupted run: WasInterrupted = false, want true")
			}
			if ir.Status != RunStatusRunning {
				t.Fatalf("interrupted run Status = %q, want running", ir.Status)
			}
			if !ir.CreatedAt.Equal(fixedClock) {
				t.Fatalf("interrupted run CreatedAt = %v, want %v", ir.CreatedAt, fixedClock)
			}

			// Recover mutates no status.
			got, err := repoB.GetRun(ctx, interrupted)
			if err != nil {
				t.Fatalf("GetRun(interrupted): %v", err)
			}
			if got.Status != RunStatusRunning {
				t.Fatalf("interrupted run status after Recover = %q, want running (unmutated)", got.Status)
			}
			got, err = repoB.GetRun(ctx, completed)
			if err != nil {
				t.Fatalf("GetRun(completed): %v", err)
			}
			if got.Status != RunStatusSucceeded {
				t.Fatalf("completed run status after Recover = %q, want succeeded (unmutated)", got.Status)
			}

			// repoA's claim on the INTERRUPTED run is still held.
			requireErr(t, repoB.ClaimRun(ctx, interrupted, "fresh-holder"),
				ErrClaimHeld, "claim interrupted run after Recover")
		})
	}
}
