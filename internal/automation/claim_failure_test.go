package automation

// Claim and checkpoint failure arms reached by removing the tables these
// writes target underneath an open handle. The scheduler's whole
// self-healing story lives in these branches: a lookup or conditional
// write that fails must log or wrap rather than wedge a run in RunRunning
// forever.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestInterruptOrphanedRunSurvivesALookupFailure covers the
// GetRunningAutomationRunByClaimToken error arm. It is deliberately a log,
// not a return: this runs inside a stale-claim takeover that has already
// been admitted, so a lookup fault must never block the fire it just won.
func TestInterruptOrphanedRunSurvivesALookupFailure(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("drop automation_runs: %v", err)
	}

	// Must not panic and must not propagate: the takeover continues.
	svc.interruptOrphanedRun(ctx, "auto-lookup-fail", "some-token")
}

// TestCheckpointRunFencedWrapsARereadFailure covers checkpointRunFenced's
// reread arm: when the fenced write matches no row the code rereads to
// tell "fenced out" from "gone", and a failure of that reread must be
// wrapped with the run id rather than swallowed into a bare fence error.
func TestCheckpointRunFencedWrapsARereadFailure(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	run := Run{
		ID:           "checkpoint-reread-run",
		AutomationID: "checkpoint-auto",
		State:        RunRunning,
		StartedAt:    time.Now().UTC(),
		ClaimToken:   "token-1",
	}
	if err := svc.createRun(ctx, run); err != nil {
		t.Fatalf("createRun: %v", err)
	}

	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("drop automation_runs: %v", err)
	}

	err := svc.checkpointRunFenced(ctx, run, 1)
	if err == nil {
		t.Fatal("checkpointRunFenced against a dropped table succeeded, want an error")
	}
	if !strings.Contains(err.Error(), run.ID) {
		t.Fatalf("error = %v, want it to name the run id", err)
	}
}

// admitFire's ErrClaimNotHeld arm (claim.go:119-130) is NOT covered, and
// is deliberately not excused in .mivia/policy/diff-coverage.json. It
// needs the claim row to be HELD when ClaimRunFenced runs and GONE when
// TakeoverExpiredClaimFenced runs a few statements later, inside one
// admitFire call. An external delete cannot land in that window: deleting
// before the call makes the first ClaimRunFenced succeed outright, which
// is the path TestAdmitFireRetriesWhenTheHeldClaimVanishes already takes.
// Driving it needs a store seam that can fail or mutate between the two
// calls. That is a reason the test does not exist yet, not a proof the
// branch is unreachable.
//
// sweepInterrupted's mark-failure wrap (claim.go:245) is uncovered for a
// related reason: the sweep LISTs running rows before it writes, so
// dropping the table fails the list first and the mark arm is never
// reached.
