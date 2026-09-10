package automation

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func newTestDB(t *testing.T) *storage.SQLite {
	t.Helper()
	db, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestService(t *testing.T, db *storage.SQLite) *Service {
	t.Helper()
	svc, err := New(t.TempDir(), db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

// assertRunFieldsMatch compares every field of got against want (a Run
// passed to createRun) plus the expected EndedAt, factored out so the
// round-trip test stays under the per-function LOC cap.
func assertRunFieldsMatch(t *testing.T, got, want Run, wantEnded time.Time) {
	t.Helper()
	if got.ID != want.ID || got.AutomationID != want.AutomationID || got.Origin != want.Origin ||
		got.State != want.State || got.StepIndex != want.StepIndex || got.StepCount != want.StepCount ||
		got.SessionName != want.SessionName || got.WorktreePath != want.WorktreePath ||
		got.WorktreeBranch != want.WorktreeBranch || got.ClaimToken != want.ClaimToken ||
		got.FailKind != want.FailKind || got.Message != want.Message {
		t.Fatalf("run round-trip mismatch:\n got  = %+v\n want = %+v", got, want)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(wantEnded) {
		t.Fatalf("EndedAt = %v, want %v", got.EndedAt, wantEnded)
	}
}

// TestCreateGetListRunsRoundTrip covers the plan's round-trip test:
// create a run, read it back via getRun, assert every field matches;
// listRuns returns runs ordered started_at descending and respects
// limit.
func TestCreateGetListRunsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	base := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	ended := base.Add(5 * time.Minute)
	run := Run{
		ID:             "run-1",
		AutomationID:   "auto-1",
		Origin:         "manual",
		State:          RunSucceeded,
		StepIndex:      2,
		StepCount:      3,
		SessionName:    "__auto__auto-1__run-1",
		WorktreePath:   "/tmp/wt",
		WorktreeBranch: "auto-branch",
		ClaimToken:     "claim-token",
		StartedAt:      base,
		EndedAt:        &ended,
		FailKind:       RunFailNone,
		Message:        "all steps ok",
	}
	if err := svc.createRun(ctx, run); err != nil {
		t.Fatalf("createRun: %v", err)
	}

	got, ok, err := svc.getRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("getRun: %v", err)
	}
	if !ok {
		t.Fatal("getRun: not found, want found")
	}
	assertRunFieldsMatch(t, got, run, ended)

	// Seed two more runs at different started_at times to prove ordering
	// and limit.
	run2 := run
	run2.ID = "run-2"
	run2.StartedAt = base.Add(time.Hour)
	run2.EndedAt = nil
	if err := svc.createRun(ctx, run2); err != nil {
		t.Fatalf("createRun run-2: %v", err)
	}
	run3 := run
	run3.ID = "run-3"
	run3.StartedAt = base.Add(2 * time.Hour)
	run3.EndedAt = nil
	if err := svc.createRun(ctx, run3); err != nil {
		t.Fatalf("createRun run-3: %v", err)
	}

	all, err := svc.listRuns(ctx, "auto-1", 0)
	if err != nil {
		t.Fatalf("listRuns: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("listRuns = %d entries, want 3", len(all))
	}
	if all[0].ID != "run-3" || all[1].ID != "run-2" || all[2].ID != "run-1" {
		t.Fatalf("listRuns order = %v, want [run-3 run-2 run-1] (started_at descending)", []string{all[0].ID, all[1].ID, all[2].ID})
	}

	limited, err := svc.listRuns(ctx, "auto-1", 2)
	if err != nil {
		t.Fatalf("listRuns with limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("listRuns with limit=2 = %d entries, want 2", len(limited))
	}
	if limited[0].ID != "run-3" || limited[1].ID != "run-2" {
		t.Fatalf("listRuns with limit order = %v, want [run-3 run-2]", []string{limited[0].ID, limited[1].ID})
	}
}

// TestCreateRunInvalidIDRejected proves createRun enforces D11's ID
// validation on both id and automationID before ever touching the store.
func TestCreateRunInvalidIDRejected(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	if err := svc.createRun(ctx, Run{ID: "../bad", AutomationID: "auto-1"}); err == nil {
		t.Fatal("createRun with invalid ID: got nil error, want rejection")
	}
	if err := svc.createRun(ctx, Run{ID: "run-1", AutomationID: "__last__"}); err == nil {
		t.Fatal("createRun with invalid automationID: got nil error, want rejection")
	}
}

// TestUpdateRunStateTransitions proves updateRunState moves a run through
// pending->running->succeeded and that each transition is reflected in
// getRun/listRuns.
func TestUpdateRunStateTransitions(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	started := time.Now().UTC()
	if err := svc.createRun(ctx, Run{
		ID: "run-x", AutomationID: "auto-x", Origin: "manual",
		State: RunPending, StepCount: 2, StartedAt: started,
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}

	if err := svc.updateRunState(ctx, "run-x", RunRunning, 0, nil, RunFailNone, ""); err != nil {
		t.Fatalf("updateRunState -> running: %v", err)
	}
	got, ok, err := svc.getRun(ctx, "run-x")
	if err != nil || !ok {
		t.Fatalf("getRun after running transition: ok=%v err=%v", ok, err)
	}
	if got.State != RunRunning || got.StepIndex != 0 || got.EndedAt != nil {
		t.Fatalf("after running transition = %+v, want State=running StepIndex=0 EndedAt=nil", got)
	}

	if err := svc.updateRunState(ctx, "run-x", RunRunning, 1, nil, RunFailNone, ""); err != nil {
		t.Fatalf("updateRunState -> step 1: %v", err)
	}
	got, _, _ = svc.getRun(ctx, "run-x")
	if got.StepIndex != 1 {
		t.Fatalf("after step advance StepIndex = %d, want 1", got.StepIndex)
	}

	ended := time.Now().UTC()
	if err := svc.updateRunState(ctx, "run-x", RunSucceeded, 2, &ended, RunFailNone, "done"); err != nil {
		t.Fatalf("updateRunState -> succeeded: %v", err)
	}
	got, _, _ = svc.getRun(ctx, "run-x")
	if got.State != RunSucceeded || got.StepIndex != 2 || got.EndedAt == nil || got.Message != "done" {
		t.Fatalf("after succeeded transition = %+v, want State=succeeded StepIndex=2 EndedAt!=nil Message=done", got)
	}

	// Reflected in listRuns too.
	list, err := svc.listRuns(ctx, "auto-x", 10)
	if err != nil {
		t.Fatalf("listRuns: %v", err)
	}
	if len(list) != 1 || list[0].State != RunSucceeded {
		t.Fatalf("listRuns after transitions = %+v, want one succeeded run", list)
	}
}

// TestUpdateRunStateNotFound proves updateRunState on an unknown run ID
// returns a named error rather than silently succeeding.
func TestUpdateRunStateNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	if err := svc.updateRunState(context.Background(), "no-such-run", RunRunning, 0, nil, RunFailNone, ""); err == nil {
		t.Fatal("updateRunState on missing run: got nil error, want rejection")
	}
}

// TestGetRunNotFound proves getRun reports (Run{}, false, nil) for an
// unknown ID rather than an error.
func TestGetRunNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	got, ok, err := svc.getRun(context.Background(), "no-such-run")
	if err != nil {
		t.Fatalf("getRun on missing run: got error %v, want nil", err)
	}
	if ok {
		t.Fatalf("getRun on missing run: found %+v, want not found", got)
	}
}

// TestNilDBRunStoreMethodsGracefulNeverPanic proves every runstore method
// on a Service built with db=nil returns the documented graceful
// empty/error result and never panics.
func TestNilDBRunStoreMethodsGracefulNeverPanic(t *testing.T) {
	svc := newTestService(t, nil)
	ctx := context.Background()

	if err := svc.createRun(ctx, Run{ID: "run-1", AutomationID: "auto-1"}); err == nil {
		t.Fatal("createRun with nil db: got nil error, want errNoRunStore")
	}
	if err := svc.updateRunState(ctx, "run-1", RunRunning, 0, nil, RunFailNone, ""); err == nil {
		t.Fatal("updateRunState with nil db: got nil error, want errNoRunStore")
	}
	if _, ok, err := svc.getRun(ctx, "run-1"); ok || err != nil {
		t.Fatalf("getRun with nil db = (ok=%v err=%v), want (false, nil)", ok, err)
	}
	if runs, err := svc.listRuns(ctx, "auto-1", 10); runs != nil || err != nil {
		t.Fatalf("listRuns with nil db = (%v, %v), want (nil, nil)", runs, err)
	}
	if _, ok, err := svc.admitFire(ctx, "auto-1"); ok || err == nil {
		t.Fatalf("admitFire with nil db = (ok=%v err=%v), want (false, non-nil)", ok, err)
	}
	if n, err := svc.sweepInterrupted(ctx, time.Minute); n != 0 || err != nil {
		t.Fatalf("sweepInterrupted with nil db = (%d, %v), want (0, nil)", n, err)
	}

	// Public surface must not panic either.
	if got := svc.Runs("auto-1", 10); got != nil {
		t.Fatalf("Runs() with nil db = %v, want nil", got)
	}
	if _, ok := svc.Run("run-1"); ok {
		t.Fatal("Run() with nil db found a run, want not found")
	}
}

// TestServiceRunsAndRunSurfaceSeededRun proves the public Runs()/Run()
// methods on service.go now call through to the real store: seed a run
// via createRun directly, then assert Runs(automationID, limit)/Run(runID)
// surface it correctly through the ports.Run mapping.
func TestServiceRunsAndRunSurfaceSeededRun(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	started := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	if err := svc.createRun(ctx, Run{
		ID: "seeded-run", AutomationID: "seeded-auto", Origin: "scheduled",
		State: RunFailed, StepIndex: 1, StepCount: 2,
		StartedAt: started, EndedAt: &ended,
		FailKind: RunFailJobError, Message: "boom",
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}

	runs := svc.Runs("seeded-auto", 10)
	if len(runs) != 1 {
		t.Fatalf("Runs() = %d entries, want 1: %+v", len(runs), runs)
	}
	got := runs[0]
	if got.ID != "seeded-run" || got.AutomationID != "seeded-auto" {
		t.Fatalf("Runs()[0] identity = %+v", got)
	}
	if got.Trigger != ports.TriggerScheduled {
		t.Fatalf("Runs()[0].Trigger = %v, want TriggerScheduled", got.Trigger)
	}
	if got.State != ports.RunFailed {
		t.Fatalf("Runs()[0].State = %v, want RunFailed", got.State)
	}
	if got.FailKind != ports.RunFailJobError {
		t.Fatalf("Runs()[0].FailKind = %v, want RunFailJobError", got.FailKind)
	}
	if got.Message != "boom" {
		t.Fatalf("Runs()[0].Message = %q, want boom", got.Message)
	}
	if !got.StartedAt.Equal(started) {
		t.Fatalf("Runs()[0].StartedAt = %v, want %v", got.StartedAt, started)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Fatalf("Runs()[0].EndedAt = %v, want %v", got.EndedAt, ended)
	}

	one, ok := svc.Run("seeded-run")
	if !ok {
		t.Fatal("Run(seeded-run): not found, want found")
	}
	if one.ID != "seeded-run" || one.State != ports.RunFailed {
		t.Fatalf("Run(seeded-run) = %+v", one)
	}

	if _, ok := svc.Run("no-such-run"); ok {
		t.Fatal("Run(no-such-run): found, want not found")
	}
}

// TestRunStateAndFailKindMappingDefaults pins the default branches of
// runStateToPorts/runFailKindToPorts/runOriginToTrigger for out-of-range
// or unset inputs.
func TestRunStateAndFailKindMappingDefaults(t *testing.T) {
	if got := runStateToPorts(RunState("bogus")); got != ports.RunPending {
		t.Fatalf("runStateToPorts(bogus) = %v, want RunPending", got)
	}
	if got := runFailKindToPorts(RunFailKind("bogus")); got != ports.RunFailNone {
		t.Fatalf("runFailKindToPorts(bogus) = %v, want RunFailNone", got)
	}
	if got := runOriginToTrigger("bogus"); got != ports.TriggerManual {
		t.Fatalf("runOriginToTrigger(bogus) = %v, want TriggerManual", got)
	}
	if got := runOriginToTrigger("scheduled"); got != ports.TriggerScheduled {
		t.Fatalf("runOriginToTrigger(scheduled) = %v, want TriggerScheduled", got)
	}
}

// TestRunStateToPortsCoversEveryNamedCase pins every non-default branch
// of runStateToPorts, including RunSkipped (chunk 6's D7 addition) -
// TestRunStateAndFailKindMappingDefaults above only exercises the
// default (unknown-input) branch, so this closes the remaining named
// cases.
func TestRunStateToPortsCoversEveryNamedCase(t *testing.T) {
	cases := []struct {
		in   RunState
		want ports.RunState
	}{
		{RunRunning, ports.RunRunning},
		{RunSucceeded, ports.RunSucceeded},
		{RunFailed, ports.RunFailed},
		{RunCancelled, ports.RunCancelled},
		{RunInterrupted, ports.RunInterrupted},
		{RunSkipped, ports.RunSkipped},
	}
	for _, tc := range cases {
		if got := runStateToPorts(tc.in); got != tc.want {
			t.Errorf("runStateToPorts(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestRunFailKindToPortsCoversEveryNamedCase pins every non-default
// branch of runFailKindToPorts - only RunFailJobError was previously
// exercised (indirectly, via other tests); RunFailConditionNotMet and
// RunFailTimeout had no direct coverage at all.
func TestRunFailKindToPortsCoversEveryNamedCase(t *testing.T) {
	cases := []struct {
		in   RunFailKind
		want ports.RunFailKind
	}{
		{RunFailJobError, ports.RunFailJobError},
		{RunFailConditionNotMet, ports.RunFailConditionNotMet},
		{RunFailTimeout, ports.RunFailTimeout},
	}
	for _, tc := range cases {
		if got := runFailKindToPorts(tc.in); got != tc.want {
			t.Errorf("runFailKindToPorts(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestAdmitFireDedupConcurrent is the plan's dedup negative test: two
// concurrent admitFire calls for the same automationID against the same
// *storage.SQLite -> exactly one wins (ok=true), the other is a no-op
// with nil error (ok=false, err=nil).
func TestAdmitFireDedupConcurrent(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)

	const automationID = "contended-automation"
	var wg sync.WaitGroup
	results := make([]struct {
		ok  bool
		err error
	}, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, ok, err := svc.admitFire(context.Background(), automationID)
			results[i].ok = ok
			results[i].err = err
		}(i)
	}
	wg.Wait()

	wins, losses := 0, 0
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("admitFire returned an error, want nil (lost claim is a no-op): %v", r.err)
		}
		if r.ok {
			wins++
		} else {
			losses++
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("admitFire concurrent results: wins=%d losses=%d, want 1/1", wins, losses)
	}
}

// TestAdmitFireSecondCallAfterReleaseWins proves admitFire is not
// permanently exhausted: once the holder releases the claim, a later
// admitFire for the same automationID succeeds.
func TestAdmitFireSecondCallAfterReleaseWins(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	holder1, ok, err := svc.admitFire(ctx, "auto-release")
	if err != nil || !ok {
		t.Fatalf("first admitFire: ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	if _, ok, err := svc.admitFire(ctx, "auto-release"); err != nil || ok {
		t.Fatalf("second admitFire while first is held: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	claim, err := db.GetClaim(ctx, claimKey("auto-release"))
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if err := db.ReleaseClaimFenced(ctx, claim); err != nil {
		t.Fatalf("ReleaseClaimFenced: %v", err)
	}
	holder2, ok, err := svc.admitFire(ctx, "auto-release")
	if err != nil || !ok {
		t.Fatalf("admitFire after release: ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	if holder1 == holder2 {
		t.Fatal("admitFire after release reused the same holder token, want a fresh one")
	}
}

// TestAdmitFireInvalidIDRejected proves admitFire enforces D11's ID
// validation.
func TestAdmitFireInvalidIDRejected(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	if _, ok, err := svc.admitFire(context.Background(), "__last__"); err == nil || ok {
		t.Fatalf("admitFire with invalid id: ok=%v err=%v, want ok=false err!=nil", ok, err)
	}
}

// TestSweepInterruptedFlagsExpiredClaim is the plan's interrupted-sweep
// test, re-scoped per this chunk's actual design (see runstore_test.go's
// package doc / the builder's report): D13 says an interrupted run is
// resumable, not a terminal failure, so this asserts RunInterrupted
// (ports.RunInterrupted), NOT RunFailed/RunFailJobError as the plan's
// Tests-section prose literally said - that prose predates the state
// model's finalization (RunInterrupted did not exist in ports until this
// chunk added it). sweepInterrupted returns 1 and the run's state is
// RunInterrupted afterward.
func TestSweepInterruptedFlagsExpiredClaim(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	started := time.Now().UTC().Add(-time.Hour)
	if err := svc.createRun(ctx, Run{
		ID: "stuck-run", AutomationID: "stuck-auto", Origin: "scheduled",
		State: RunRunning, StepIndex: 0, StepCount: 3, StartedAt: started,
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	// Claim the automation, then let it "expire" by using a maxAge of 0
	// (any age is >= 0, so the very next sweep call sees it as expired) -
	// avoids a real sleep in the test.
	if _, ok, err := svc.admitFire(ctx, "stuck-auto"); err != nil || !ok {
		t.Fatalf("admitFire (simulating the crashed holder's claim): ok=%v err=%v", ok, err)
	}

	n, err := svc.sweepInterrupted(ctx, 0)
	if err != nil {
		t.Fatalf("sweepInterrupted: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweepInterrupted returned %d, want 1", n)
	}

	got, ok, err := svc.getRun(ctx, "stuck-run")
	if err != nil || !ok {
		t.Fatalf("getRun after sweep: ok=%v err=%v", ok, err)
	}
	if got.State != RunInterrupted {
		t.Fatalf("state after sweep = %v, want RunInterrupted", got.State)
	}
	if got.EndedAt == nil {
		t.Fatal("EndedAt after sweep is nil, want set (terminal-ish transition)")
	}

	// Surfaced through ports too.
	one, ok := svc.Run("stuck-run")
	if !ok || one.State != ports.RunInterrupted {
		t.Fatalf("Run(stuck-run) after sweep = (%+v, %v), want RunInterrupted", one, ok)
	}

	// The claim was released by the sweep, so a fresh admitFire on the
	// same automation now succeeds (proves sweepInterrupted does not
	// leave the claim held for itself).
	if _, ok, err := svc.admitFire(ctx, "stuck-auto"); err != nil || !ok {
		t.Fatalf("admitFire after sweep released the claim: ok=%v err=%v, want ok=true err=nil", ok, err)
	}
}

// TestSweepInterruptedSkipsRunningClaimNotExpired proves a running run
// whose claim is fresh (not past maxAge) is left alone by the sweep.
func TestSweepInterruptedSkipsRunningClaimNotExpired(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	if err := svc.createRun(ctx, Run{
		ID: "fresh-run", AutomationID: "fresh-auto", Origin: "manual",
		State: RunRunning, StepCount: 1, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if _, ok, err := svc.admitFire(ctx, "fresh-auto"); err != nil || !ok {
		t.Fatalf("admitFire: ok=%v err=%v", ok, err)
	}

	n, err := svc.sweepInterrupted(ctx, time.Hour)
	if err != nil {
		t.Fatalf("sweepInterrupted: %v", err)
	}
	if n != 0 {
		t.Fatalf("sweepInterrupted returned %d, want 0 (claim not expired)", n)
	}
	got, ok, err := svc.getRun(ctx, "fresh-run")
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if got.State != RunRunning {
		t.Fatalf("state after sweep (not expired) = %v, want RunRunning (unchanged)", got.State)
	}
}

// TestSweepInterruptedFlagsRunningWithNoClaim proves a "running" row with
// no claim row at all (e.g. the process crashed before ever claiming, or
// the claim was force-cleared) is treated as interrupted too.
func TestSweepInterruptedFlagsRunningWithNoClaim(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	if err := svc.createRun(ctx, Run{
		ID: "orphan-run", AutomationID: "orphan-auto", Origin: "manual",
		State: RunRunning, StepCount: 1, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	// No admitFire call at all: no claim row exists for orphan-auto.

	n, err := svc.sweepInterrupted(ctx, time.Hour)
	if err != nil {
		t.Fatalf("sweepInterrupted: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweepInterrupted returned %d, want 1 (no-claim row treated as interrupted)", n)
	}
	got, ok, err := svc.getRun(ctx, "orphan-run")
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if got.State != RunInterrupted {
		t.Fatalf("state = %v, want RunInterrupted", got.State)
	}
}

// TestSweepInterruptedNoRunningRuns proves a sweep with nothing in
// RunRunning is a clean 0, nil.
func TestSweepInterruptedNoRunningRuns(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	n, err := svc.sweepInterrupted(context.Background(), time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("sweepInterrupted on empty store = (%d, %v), want (0, nil)", n, err)
	}
}
