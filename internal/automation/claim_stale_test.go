package automation

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestAdmitFireTakesOverStaleClaim proves the confirmed wedge fix: a
// claim held by a holder that vanished without releasing it (the crash
// case) is no longer a permanent block on every future admitFire call
// for that automation. admitFire falls back to the SAME
// storage.TakeoverExpiredClaimFenced primitive/threshold
// (defaultSweepMaxAge) sweepInterrupted already uses, by backdating the
// claim row's acquired_at directly - the same technique
// TestMemoryFencedLeaseHandlesInvalidTimestampAndFenceWrap
// (internal/storage/fenced_lease_test.go) uses to simulate age without a
// real sleep.
func TestAdmitFireTakesOverStaleClaim(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	const automationID = "stale-claim-auto"
	firstHolder, ok, err := svc.admitFire(ctx, automationID)
	if err != nil || !ok {
		t.Fatalf("first admitFire: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	// Simulate the holder crashing without ever releasing: backdate the
	// claim's acquired_at past defaultSweepMaxAge directly on the
	// store, exactly like sweepInterrupted's own maxAge=0 test does
	// (TestSweepInterruptedFlagsExpiredClaim uses maxAge 0 instead;
	// this test asserts against the REAL production threshold
	// admitFire now applies, so it backdates the row instead).
	if err := backdateClaim(t, db, claimKey(automationID), defaultSweepMaxAge+time.Minute); err != nil {
		t.Fatalf("backdateClaim: %v", err)
	}

	secondHolder, ok, err := svc.admitFire(ctx, automationID)
	if err != nil {
		t.Fatalf("admitFire against a stale claim: err = %v, want nil (atomic takeover)", err)
	}
	if !ok {
		t.Fatal("admitFire against a stale claim: ok = false, want true (stale claim must be taken over, not treated as a permanent wedge)")
	}
	if secondHolder == "" || secondHolder == firstHolder {
		t.Fatalf("admitFire after stale takeover returned holder %q (first was %q), want a fresh non-empty token", secondHolder, firstHolder)
	}

	// The old holder is genuinely fenced out: a release attempt using
	// the stale token must fail, proving the row's fence actually
	// rotated rather than merely being overwritten in place.
	if err := db.ReleaseClaim(ctx, claimKey(automationID), firstHolder); err == nil {
		t.Fatal("ReleaseClaim with the fenced-out original holder succeeded, want an error (claim ownership must have rotated)")
	}

	// The new holder legitimately owns the claim now.
	claim, err := db.GetClaim(ctx, claimKey(automationID))
	if err != nil {
		t.Fatalf("GetClaim after takeover: %v", err)
	}
	if claim.Holder != secondHolder {
		t.Fatalf("claim holder after takeover = %q, want %q", claim.Holder, secondHolder)
	}
}

// TestAdmitFireStillBlocksFreshClaim proves the fix is scoped to stale
// claims only: a claim acquired moments ago (well under
// defaultSweepMaxAge) still produces the documented no-op (ok=false,
// err=nil), exactly as before this fix - a genuinely in-flight run must
// not be preempted.
func TestAdmitFireStillBlocksFreshClaim(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	const automationID = "fresh-claim-auto"
	firstHolder, ok, err := svc.admitFire(ctx, automationID)
	if err != nil || !ok {
		t.Fatalf("first admitFire: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	secondHolder, ok, err := svc.admitFire(ctx, automationID)
	if err != nil {
		t.Fatalf("admitFire against a fresh claim: err = %v, want nil (documented no-op)", err)
	}
	if ok {
		t.Fatal("admitFire against a fresh claim: ok = true, want false (a genuinely in-flight claim must not be preempted)")
	}
	if secondHolder != "" {
		t.Fatalf("admitFire against a fresh claim returned holder %q, want empty on refusal", secondHolder)
	}

	// The original holder is untouched.
	claim, err := db.GetClaim(ctx, claimKey(automationID))
	if err != nil {
		t.Fatalf("GetClaim after refused takeover: %v", err)
	}
	if claim.Holder != firstHolder {
		t.Fatalf("claim holder after refused takeover = %q, want unchanged %q", claim.Holder, firstHolder)
	}
}

// TestRunOnceRecoversFromStaleClaimAndFreshRunSucceeds is the RunOnce
// (manual, CLI-facing) regression for the same wedge: a stale claim left
// behind by a crashed run must not permanently block RunOnce - a later
// manual fire must actually execute (RunSucceeded), not just report the
// admission win.
func TestRunOnceRecoversFromStaleClaimAndFreshRunSucceeds(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil)
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// Simulate a crashed prior fire: win the claim, then never release
	// it (no RunOnce ever completes for this token).
	if _, ok, err := svc.admitFire(ctx, automationID); err != nil || !ok {
		t.Fatalf("pre-acquire admitFire (simulating the crashed holder's claim): ok=%v err=%v", ok, err)
	}
	if err := backdateClaim(t, db, claimKey(automationID), defaultSweepMaxAge+time.Minute); err != nil {
		t.Fatalf("backdateClaim: %v", err)
	}

	run, err := svc.RunOnce(ctx, automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce against a stale claim: err = %v, want nil", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("RunOnce against a stale claim: state = %v, want RunSucceeded (the stale claim must be recovered, not skipped)", run.State)
	}
	if spawn.createCallCount() != 1 {
		t.Fatalf("RunOnce against a stale claim: spawn.CreateFreshInDir called %d times, want 1 (the recovered fire must actually execute)", spawn.createCallCount())
	}
}

// TestRunOnceStillSkipsGenuinelyActiveClaim is RunOnce's fresh-claim
// counterpart: a claim acquired moments ago must still produce the
// documented RunSkipped no-op, unaffected by this fix.
func TestRunOnceStillSkipsGenuinelyActiveClaim(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil)
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if _, ok, err := svc.admitFire(ctx, automationID); err != nil || !ok {
		t.Fatalf("pre-acquire admitFire: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	run, err := svc.RunOnce(ctx, automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce against a fresh claim: err = %v, want nil (documented no-op)", err)
	}
	if run.State != ports.RunSkipped {
		t.Fatalf("RunOnce against a fresh claim: state = %v, want RunSkipped", run.State)
	}
	if spawn.createCallCount() != 0 {
		t.Fatalf("RunOnce against a fresh claim: spawn.CreateFreshInDir called %d times, want 0", spawn.createCallCount())
	}
}

// TestResumeRunRecoversFromStaleClaim proves ResumeRun's own admission
// (admitResume, which calls the SAME admitFire this fix changed) also
// lazily recovers a stale claim rather than staying sweep-gated. Before
// this fix, a crashed run's stale claim wedged ResumeRun identically to
// RunOnce: the operator's only path back was waiting for (or manually
// invoking) SweepInterrupted first. admitResume calls admitFire
// directly with no sweep in between, so this proves the recovery is
// real for the resume path too, not just RunOnce's.
func TestResumeRunRecoversFromStaleClaim(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	sess := newContextEnabledSession(t, db)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	automationID, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one"}, RunInterrupted, 0, true)

	// Simulate a second run (or a stray leftover claim from a different
	// crashed process) holding the automation's claim without ever
	// releasing it.
	if _, ok, err := svc.admitFire(context.Background(), automationID); err != nil || !ok {
		t.Fatalf("pre-acquire admitFire (simulating a crashed holder's claim): ok=%v err=%v", ok, err)
	}
	if err := backdateClaim(t, db, claimKey(automationID), defaultSweepMaxAge+time.Minute); err != nil {
		t.Fatalf("backdateClaim: %v", err)
	}

	if _, err := svc.ResumeRun(context.Background(), runID); err != nil {
		t.Fatalf("ResumeRun against a stale claim: err = %v, want nil (recovered, not ErrRunAlreadyActive)", err)
	}
	got, ok, err := svc.getRun(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("getRun after resume: ok=%v err=%v", ok, err)
	}
	if got.State != RunSucceeded {
		t.Fatalf("run state after resuming through a stale claim = %v, want RunSucceeded", got.State)
	}
}

// TestResumeRunStillRefusesGenuinelyActiveClaim proves
// TestResumeRunHeldClaimReturnsErrRunAlreadyActive's existing fresh-claim
// refusal is unaffected by this fix - restated here beside the new
// stale-claim recovery test so the two cases sit next to each other.
func TestResumeRunStillRefusesGenuinelyActiveClaim(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	sess := newContextEnabledSession(t, db)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	automationID, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one"}, RunInterrupted, 0, true)

	if _, ok, err := svc.admitFire(context.Background(), automationID); err != nil || !ok {
		t.Fatalf("pre-acquire admitFire: ok=%v err=%v", ok, err)
	}

	if _, err := svc.ResumeRun(context.Background(), runID); err == nil {
		t.Fatal("ResumeRun against a fresh, genuinely active claim: got nil error, want ErrRunAlreadyActive")
	}
}

// TestAdmitFireStaleTakeoverInterruptsOrphanedRunNotUnrelatedFreshRun is
// the confirmed orphan-row regression: a lazy stale-claim takeover in
// admitFire must mark the CRASHED holder's own run row interrupted
// (never left wedged in RunRunning), while leaving a genuinely
// unrelated, still-live run row (a different automation's own claim and
// running row) completely untouched. It exercises the full stack -
// RunOnce, not admitFire directly - so the new fire's own run row is
// verified to actually proceed to RunSucceeded, not just win admission.
//
// Setup and verification are split into helpers
// (seedOrphanedRunningRow, seedUnrelatedFreshRunningRow,
// assertOrphanRowInterrupted, assertUnrelatedRunUntouched) so each piece
// of the scenario reads and fails independently.
func TestAdmitFireStaleTakeoverInterruptsOrphanedRunNotUnrelatedFreshRun(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil)
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// Simulate a crashed prior fire that got as far as inserting and
	// running its own row (exactly what startRun, executor_run.go,
	// does) but crashed before any terminal write - the orphan-row
	// scenario: a genuinely running row whose only owning claim is
	// about to be taken over as stale.
	orphanRun := seedOrphanedRunningRow(t, ctx, svc, automationID)

	// An unrelated automation with its own genuinely fresh, still-live
	// claim and running row - the takeover below targets only
	// automationID's claim key, so this row must survive completely
	// unchanged.
	otherAutomationID, freshRun, freshHolder := seedUnrelatedFreshRunningRow(t, ctx, svc)

	// Age only automationID's claim past defaultSweepMaxAge - the
	// unrelated automation's claim is left fresh.
	if err := backdateClaim(t, db, claimKey(automationID), defaultSweepMaxAge+time.Minute); err != nil {
		t.Fatalf("backdateClaim: %v", err)
	}

	run, err := svc.RunOnce(ctx, automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce against a stale claim: err = %v, want nil", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("RunOnce against a stale claim: state = %v, want RunSucceeded (the new run must actually proceed, not just win admission)", run.State)
	}
	if run.ID == orphanRun.ID {
		t.Fatalf("RunOnce reused the orphaned row's own id %q, want a fresh run id", orphanRun.ID)
	}

	assertOrphanRowInterrupted(t, ctx, db, orphanRun.ID)
	assertUnrelatedRunUntouched(t, ctx, db, otherAutomationID, freshRun.ID, freshHolder)
}

// seedOrphanedRunningRow admits automationID's claim and inserts its own
// RunRunning row, simulating a crashed fire that never reached a
// terminal write. It returns the seeded run so the caller can assert
// against its own id.
func seedOrphanedRunningRow(t *testing.T, ctx context.Context, svc *Service, automationID string) Run {
	t.Helper()
	crashedHolder, ok, err := svc.admitFire(ctx, automationID)
	if err != nil || !ok {
		t.Fatalf("pre-acquire admitFire (simulating the crashed holder's claim): ok=%v err=%v", ok, err)
	}
	orphanRun := Run{
		ID:           "run-orphan",
		AutomationID: automationID,
		State:        RunRunning,
		StepCount:    1,
		ClaimToken:   crashedHolder,
		StartedAt:    time.Now().UTC(),
	}
	if err := svc.createRun(ctx, orphanRun); err != nil {
		t.Fatalf("createRun (orphan row): %v", err)
	}
	return orphanRun
}

// seedUnrelatedFreshRunningRow admits a genuinely fresh claim for a
// second, unrelated automation and inserts its own RunRunning row. It
// returns the automation id, the seeded run, and the winning holder
// token so the caller can assert none of the three changed.
func seedUnrelatedFreshRunningRow(t *testing.T, ctx context.Context, svc *Service) (string, Run, string) {
	t.Helper()
	const otherAutomationID = "unrelated-fresh-auto"
	freshHolder, ok, err := svc.admitFire(ctx, otherAutomationID)
	if err != nil || !ok {
		t.Fatalf("admitFire for unrelated automation: ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	freshRun := Run{
		ID:           "run-unrelated-fresh",
		AutomationID: otherAutomationID,
		State:        RunRunning,
		StepCount:    1,
		ClaimToken:   freshHolder,
		StartedAt:    time.Now().UTC(),
	}
	if err := svc.createRun(ctx, freshRun); err != nil {
		t.Fatalf("createRun (unrelated fresh row): %v", err)
	}
	return otherAutomationID, freshRun, freshHolder
}

// assertOrphanRowInterrupted verifies the crashed holder's own row was
// closed out (state and ended_at), never left wedged in RunRunning.
func assertOrphanRowInterrupted(t *testing.T, ctx context.Context, db *storage.SQLite, orphanRunID string) {
	t.Helper()
	orphanRow, ok, gerr := db.GetAutomationRun(ctx, orphanRunID)
	if gerr != nil || !ok {
		t.Fatalf("GetAutomationRun(orphan): ok=%v err=%v", ok, gerr)
	}
	if orphanRow.State != string(RunInterrupted) {
		t.Fatalf("orphaned row state = %q, want %q (a stale-claim takeover must not leave the prior holder's run stuck Running)", orphanRow.State, RunInterrupted)
	}
	if orphanRow.EndedAt == nil {
		t.Fatal("orphaned row EndedAt = nil, want set on interrupt")
	}
}

// assertUnrelatedRunUntouched verifies the unrelated automation's
// genuinely fresh running row and claim survived the takeover
// completely unchanged: same state, same claim token, same holder.
func assertUnrelatedRunUntouched(t *testing.T, ctx context.Context, db *storage.SQLite, otherAutomationID, freshRunID, freshHolder string) {
	t.Helper()
	otherRow, ok, gerr := db.GetAutomationRun(ctx, freshRunID)
	if gerr != nil || !ok {
		t.Fatalf("GetAutomationRun(unrelated fresh): ok=%v err=%v", ok, gerr)
	}
	if otherRow.State != string(RunRunning) {
		t.Fatalf("unrelated fresh row state = %q, want unchanged %q", otherRow.State, RunRunning)
	}
	if otherRow.ClaimToken != freshHolder {
		t.Fatalf("unrelated fresh row claim token = %q, want unchanged %q", otherRow.ClaimToken, freshHolder)
	}
	otherClaim, cerr := db.GetClaim(ctx, claimKey(otherAutomationID))
	if cerr != nil {
		t.Fatalf("GetClaim(unrelated): %v", cerr)
	}
	if otherClaim.Holder != freshHolder {
		t.Fatalf("unrelated automation's claim holder = %q, want unchanged %q", otherClaim.Holder, freshHolder)
	}
}

// backdateClaim opens a second, independent connection to db's
// underlying SQLite file and rewrites claimKey's acquired_at to
// time.Now()-age, so a subsequent TakeoverExpiredClaimFenced call with a
// maxAge less than age sees it as expired - the same "no real sleep"
// technique dropRunClaimsTable/dropAutomationRunsTable (runstore_test.go)
// use for schema-level fault injection, applied here to one row's data
// instead.
func backdateClaim(t *testing.T, db *storage.SQLite, claimKeyStr string, age time.Duration) error {
	t.Helper()
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		return err
	}
	defer raw.Close()
	backdated := time.Now().UTC().Add(-age).Format(time.RFC3339Nano)
	_, err = raw.Exec(`UPDATE run_claims SET acquired_at = ? WHERE run_id = ?`, backdated, claimKeyStr)
	return err
}
