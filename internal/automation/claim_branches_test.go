package automation

// Admission-race and orphan-cleanup branches of claim.go. These paths only
// run when a claim row changes underneath admitFire between its two reads,
// or when a stale takeover finds no matching run row to close out. They are
// the difference between a crashed holder wedging every future fire and the
// scheduler self-healing, so each branch is driven directly here rather
// than left to a timing race.

import (
	"context"
	"testing"
	"time"
)

// TestAdmitFireRetriesWhenTheHeldClaimVanishes covers admitFire's
// ErrClaimNotHeld arm: the claim looked held on the first attempt, but its
// holder released it before the takeover ran. Nothing owns it at that
// point, so admission must retry the plain claim and succeed rather than
// report a spurious loss and skip the fire.
func TestAdmitFireRetriesWhenTheHeldClaimVanishes(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	const automationID = "vanishing-claim-auto"
	key := claimKey(automationID)

	firstHolder, ok, err := svc.admitFire(ctx, automationID)
	if err != nil || !ok {
		t.Fatalf("first admitFire: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	// The holder releases normally. The claim is now absent, which is the
	// state the ErrClaimNotHeld arm exists for: a second fire must take it
	// cleanly instead of refusing.
	if err := db.ReleaseClaim(ctx, key, firstHolder); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}

	secondHolder, ok, err := svc.admitFire(ctx, automationID)
	if err != nil {
		t.Fatalf("admitFire after the claim was released: err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("admitFire after the claim was released: ok = false, want true (a released claim must be re-claimable)")
	}
	if secondHolder == "" || secondHolder == firstHolder {
		t.Fatalf("admitFire returned holder %q (first was %q), want a fresh non-empty token", secondHolder, firstHolder)
	}
}

// TestInterruptOrphanedRunIgnoresAnEmptyHolder pins the deliberate no-op:
// with no previous holder token there is nothing to match a run row
// against, and guessing which row to interrupt would close out a live run
// belonging to someone else. It must do nothing at all.
func TestInterruptOrphanedRunIgnoresAnEmptyHolder(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	root := t.TempDir()
	_ = root
	run := Run{
		ID:           "orphan-guard-run",
		AutomationID: "orphan-guard-auto",
		State:        RunRunning,
		StartedAt:    time.Now().UTC(),
		ClaimToken:   "some-live-token",
	}
	if err := svc.createRun(ctx, run); err != nil {
		t.Fatalf("createRun: %v", err)
	}

	// prevHolder == "" must leave the running row untouched.
	svc.interruptOrphanedRun(ctx, "orphan-guard-auto", "")

	got, ok, err := db.GetAutomationRun(ctx, run.ID)
	if err != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, err)
	}
	if got.State != string(RunRunning) {
		t.Fatalf("run state = %q, want it still %q (an empty holder must interrupt nothing)", got.State, RunRunning)
	}
}

// TestInterruptOrphanedRunIgnoresAnUnmatchedToken covers the !ok arm: the
// fenced-out holder owns no running row (its claim predates any run row, or
// the row already reached a terminal state). No other automation's row may
// be touched as a consequence.
func TestInterruptOrphanedRunIgnoresAnUnmatchedToken(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	live := Run{
		ID:           "unrelated-live-run",
		AutomationID: "unmatched-token-auto",
		State:        RunRunning,
		StartedAt:    time.Now().UTC(),
		ClaimToken:   "live-token",
	}
	if err := svc.createRun(ctx, live); err != nil {
		t.Fatalf("createRun: %v", err)
	}

	// A token no run row carries.
	svc.interruptOrphanedRun(ctx, "unmatched-token-auto", "token-that-owns-nothing")

	got, ok, err := db.GetAutomationRun(ctx, live.ID)
	if err != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, err)
	}
	if got.State != string(RunRunning) {
		t.Fatalf("run state = %q, want it still %q (an unmatched token must interrupt nothing)", got.State, RunRunning)
	}
}

// TestAdmitFireRejectsAnInvalidAutomationID pins the validation guard ahead
// of any claim write: a malformed id must never reach the claim store.
func TestAdmitFireRejectsAnInvalidAutomationID(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)

	if _, ok, err := svc.admitFire(context.Background(), "not a valid id!"); err == nil || ok {
		t.Fatalf("admitFire with an invalid id: ok=%v err=%v, want ok=false and an error", ok, err)
	}
}

// TestAdmitFireWithoutARunStoreFails pins the nil-store guard: admission
// without durable storage must refuse rather than silently admit a fire
// whose run can never be recorded.
func TestAdmitFireWithoutARunStoreFails(t *testing.T) {
	svc := &Service{}
	_, ok, err := svc.admitFire(context.Background(), "any-auto")
	if ok {
		t.Fatal("admitFire without a run store returned ok=true")
	}
	if err != errNoRunStore {
		t.Fatalf("err = %v, want errNoRunStore", err)
	}
}
