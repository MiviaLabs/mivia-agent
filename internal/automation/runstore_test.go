package automation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// dropRunClaimsTable opens a second, independent connection to db's
// underlying SQLite file and drops the run_claims table through it -
// storage.SQLite exposes no raw-SQL escape hatch (its *sql.DB field is
// unexported), so this is the only way a caller outside package storage
// can force TakeoverExpiredClaimFenced to fail with a real, non-sentinel
// error while automation_runs (a separate table) stays intact and
// queryable. The "sqlite" driver is registered process-wide by
// modernc.org/sqlite's blank import inside package storage (already
// linked transitively via storage.OpenSQLite), so no separate import is
// needed here.
func dropRunClaimsTable(t *testing.T, db *storage.SQLite) error {
	t.Helper()
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		return err
	}
	defer raw.Close()
	_, err = raw.Exec(`DROP TABLE run_claims`)
	return err
}

// dropAutomationRunsTable opens a second, independent connection to db's
// underlying SQLite file and drops the automation_runs table through it
// - keeps run_claims intact so admitFire's own claim win still succeeds,
// forcing a caller further downstream (createRun) to be the one that
// hits a real store error. Same rationale/technique as
// dropRunClaimsTable, inverted.
func dropAutomationRunsTable(t *testing.T, db *storage.SQLite) error {
	t.Helper()
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		return err
	}
	defer raw.Close()
	_, err = raw.Exec(`DROP TABLE automation_runs`)
	return err
}

// deleteAutomationRunRow opens a second, independent connection to db's
// underlying SQLite file and deletes one automation_runs row through it
// - surgical variant of dropAutomationRunsTable: it removes exactly one
// row rather than the whole table, so a caller further downstream that
// reads/updates the row by id hits a real "not found" failure instead
// of a table-missing failure.
func deleteAutomationRunRow(t *testing.T, db *storage.SQLite, runID string) error {
	t.Helper()
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		return err
	}
	defer raw.Close()
	_, err = raw.Exec(`DELETE FROM automation_runs WHERE id = ?`, runID)
	return err
}

// forceAutomationRunsUpdateFailures installs a SQLite trigger that fails
// every UPDATE on automation_runs (but never an INSERT), through a
// second independent connection to db's underlying file. This lets a
// test reach a code path where the FIRST store write (createRun's own
// INSERT) succeeds but the SECOND, immediately-following write (an
// UpdateAutomationRunState call) fails - a sequence with no other
// interleaving point available to a single-threaded caller, since both
// calls are synchronous Go calls inside one production function
// (startRun, RunOnce's own "mark succeeded" call) with no seam between
// them.
func forceAutomationRunsUpdateFailures(t *testing.T, db *storage.SQLite) error {
	t.Helper()
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		return err
	}
	defer raw.Close()
	_, err = raw.Exec(`CREATE TRIGGER force_automation_runs_update_fail BEFORE UPDATE ON automation_runs BEGIN SELECT RAISE(FAIL, 'test-induced update failure'); END`)
	return err
}

// forceAutomationRunsUpdateFailuresAfter installs a SQLite trigger that
// allows the first `allow` UPDATE statements on automation_runs to
// succeed, then fails every one after that - through a second
// independent connection to db's underlying file. Lets a test reach a
// code path where several earlier writes (e.g. startRun's own
// pending->running transition, then a step's own checkpoint) must
// succeed for real before a LATER write (e.g. RunOnce's own final "mark
// succeeded" call) is the one that fails - unlike
// forceAutomationRunsUpdateFailures, which fails immediately and so can
// only isolate the very next UPDATE after it is installed.
func forceAutomationRunsUpdateFailuresAfter(t *testing.T, db *storage.SQLite, allow int) error {
	t.Helper()
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		return err
	}
	defer raw.Close()
	stmts := []string{
		`CREATE TABLE test_update_counter (n INTEGER NOT NULL)`,
		`INSERT INTO test_update_counter(n) VALUES (0)`,
		fmt.Sprintf(`CREATE TRIGGER force_automation_runs_update_fail_after BEFORE UPDATE ON automation_runs
			BEGIN
				UPDATE test_update_counter SET n = n + 1;
				SELECT CASE WHEN (SELECT n FROM test_update_counter) > %d
					THEN RAISE(FAIL, 'test-induced update failure')
				END;
			END`, allow),
	}
	for _, stmt := range stmts {
		if _, err := raw.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
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

// TestCreateRunDefaultsEmptyStateToPending proves createRun's own
// zero-value normalization: a Run passed with State == "" is persisted
// (and read back) as RunPending, not an empty string.
func TestCreateRunDefaultsEmptyStateToPending(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	if err := svc.createRun(ctx, Run{ID: "run-default-state", AutomationID: "auto-default-state", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("createRun with empty State: %v", err)
	}
	got, ok, err := svc.getRun(ctx, "run-default-state")
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if got.State != RunPending {
		t.Fatalf("State after createRun with empty input State = %v, want RunPending", got.State)
	}
}

// TestCreateRunDefaultsEmptyStartedAtToNow proves toStorageRun's own
// zero-value normalization: a Run passed with a zero-value StartedAt is
// persisted with the current time, not a zero/empty timestamp -
// TestCreateRunDefaultsEmptyStateToPending above always sets StartedAt
// explicitly, so this branch had no coverage.
func TestCreateRunDefaultsEmptyStartedAtToNow(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	before := time.Now().UTC().Add(-time.Second) // RFC3339 storage truncates sub-second precision
	if err := svc.createRun(ctx, Run{ID: "run-zero-started", AutomationID: "auto-zero-started"}); err != nil {
		t.Fatalf("createRun with zero StartedAt: %v", err)
	}
	got, ok, err := svc.getRun(ctx, "run-zero-started")
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if got.StartedAt.IsZero() {
		t.Fatal("StartedAt after createRun with a zero input StartedAt is still zero, want it defaulted to now")
	}
	if got.StartedAt.Before(before) {
		t.Fatalf("StartedAt = %v, want it no earlier than the call itself (%v)", got.StartedAt, before)
	}
}

// TestCreateRunPropagatesStoreError closes the store's underlying
// connection before calling createRun, so InsertAutomationRun fails with
// a real error - exercising createRun's own error-wrap branch, distinct
// from the earlier ValidateID rejections above (which never reach the
// store at all).
func TestCreateRunPropagatesStoreError(t *testing.T) {
	db := newTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	svc, err := New(t.TempDir(), db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.createRun(context.Background(), Run{ID: "run-closed", AutomationID: "auto-closed", StartedAt: time.Now().UTC()}); err == nil {
		t.Fatal("createRun against a closed store returned nil error, want a real error")
	}
}

// TestUpdateRunStateFencedTransitions proves updateRunStateFenced moves
// a run through pending->running->succeeded while the row carries the
// writer's claim token, and that getRun/listRuns reflect each step.
func TestUpdateRunStateFencedTransitions(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	started := time.Now().UTC()
	runX := Run{
		ID: "run-x", AutomationID: "auto-x", Origin: "manual",
		State: RunPending, StepCount: 2, ClaimToken: "tok-x", StartedAt: started,
	}
	if err := svc.createRun(ctx, runX); err != nil {
		t.Fatalf("createRun: %v", err)
	}

	if err := svc.updateRunStateFenced(ctx, runX, RunRunning, 0, nil, RunFailNone, ""); err != nil {
		t.Fatalf("updateRunStateFenced -> running: %v", err)
	}
	got, ok, err := svc.getRun(ctx, "run-x")
	if err != nil || !ok {
		t.Fatalf("getRun after running transition: ok=%v err=%v", ok, err)
	}
	if got.State != RunRunning || got.StepIndex != 0 || got.EndedAt != nil {
		t.Fatalf("after running transition = %+v, want State=running StepIndex=0 EndedAt=nil", got)
	}

	if err := svc.updateRunStateFenced(ctx, runX, RunRunning, 1, nil, RunFailNone, ""); err != nil {
		t.Fatalf("updateRunStateFenced -> step 1: %v", err)
	}
	got, _, _ = svc.getRun(ctx, "run-x")
	if got.StepIndex != 1 {
		t.Fatalf("after step advance StepIndex = %d, want 1", got.StepIndex)
	}

	ended := time.Now().UTC()
	if err := svc.updateRunStateFenced(ctx, runX, RunSucceeded, 2, &ended, RunFailNone, "done"); err != nil {
		t.Fatalf("updateRunStateFenced -> succeeded: %v", err)
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

// TestUpdateRunStateFencedNotFound proves updateRunStateFenced on an
// unknown run ID reports ErrRunFenced rather than silently succeeding:
// no row carries the token, so zero rows match.
func TestUpdateRunStateFencedNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	missing := Run{ID: "no-such-run", AutomationID: "auto-x", ClaimToken: "tok"}
	if err := svc.updateRunStateFenced(context.Background(), missing, RunRunning, 0, nil, RunFailNone, ""); !errors.Is(err, ErrRunFenced) {
		t.Fatalf("updateRunStateFenced on missing run = %v, want ErrRunFenced", err)
	}
}

// TestUpdateRunStateFencedPropagatesStoreError closes the store's
// underlying connection before the write, so the store fails with a
// real error. This exercises the error-wrap branch, distinct from the
// zero-rows case above.
func TestUpdateRunStateFencedPropagatesStoreError(t *testing.T) {
	db := newTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	svc, err := New(t.TempDir(), db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	run := Run{ID: "run-x", AutomationID: "auto-x", ClaimToken: "tok"}
	err = svc.updateRunStateFenced(context.Background(), run, RunRunning, 0, nil, RunFailNone, "")
	if err == nil || errors.Is(err, ErrRunFenced) {
		t.Fatalf("updateRunStateFenced against a closed store = %v, want a real store error", err)
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

// TestListRunsPropagatesStoreError closes the store's underlying
// connection before calling listRuns, so ListAutomationRuns fails with a
// real error - exercising listRuns' own error-wrap branch.
func TestListRunsPropagatesStoreError(t *testing.T) {
	db := newTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	svc, err := New(t.TempDir(), db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.listRuns(context.Background(), "auto-1", 10); err == nil {
		t.Fatal("listRuns against a closed store returned nil error, want a real error")
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
	if err := svc.updateRunStateFenced(ctx, Run{ID: "run-1", AutomationID: "auto-1"}, RunRunning, 0, nil, RunFailNone, ""); err == nil {
		t.Fatal("updateRunStateFenced with nil db: got nil error, want errNoRunStore")
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

// TestAdmitFirePropagatesRealClaimError closes the store's underlying
// connection before calling admitFire, so ClaimRunFenced fails with a
// real, non-ErrClaimHeld error ("sql: database is closed") - exercising
// admitFire's own error-wrap branch (claim.go's "any other error is real
// and reported"), distinct from the documented ErrClaimHeld no-op branch
// TestAdmitFireDedupConcurrent/TestAdmitFireSecondCallAfterReleaseWins
// already cover.
func TestAdmitFirePropagatesRealClaimError(t *testing.T) {
	db := newTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	svc, err := New(t.TempDir(), db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, ok, err := svc.admitFire(context.Background(), "closed-db-automation")
	if ok {
		t.Fatal("admitFire against a closed store returned ok=true, want false")
	}
	if err == nil {
		t.Fatal("admitFire against a closed store returned nil error, want a real (non-ErrClaimHeld) error")
	}
}

// TestSweepInterruptedPropagatesRealTakeoverError seeds one RunRunning
// row, then drops the run_claims table before sweeping, so
// ListRunningAutomationRuns (which only touches automation_runs) still
// succeeds and finds the row, but TakeoverExpiredClaimFenced fails with a
// real, non-ErrClaimHeld/ErrClaimNotHeld error ("no such table:
// run_claims") once inside the per-row loop - exercising
// sweepInterrupted's default branch (claim.go's "any other error is
// returned, not silently treated as interrupted or in-flight"), distinct
// from a whole-store closed-DB failure (which would fail at
// ListRunningAutomationRuns itself, before the loop is ever reached).
func TestSweepInterruptedPropagatesRealTakeoverError(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()
	run := Run{ID: "run-sweep-err", AutomationID: "auto-sweep-err", State: RunRunning, StartedAt: time.Now().UTC()}
	if err := svc.createRun(ctx, run); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if _, err := db.GetClaim(ctx, "unrelated-probe"); err != nil && !errors.Is(err, storage.ErrClaimNotHeld) {
		t.Fatalf("sanity GetClaim before drop: %v", err)
	}
	if err := dropRunClaimsTable(t, db); err != nil {
		t.Fatalf("drop run_claims table: %v", err)
	}
	_, err := svc.sweepInterrupted(ctx, time.Minute)
	if err == nil {
		t.Fatal("sweepInterrupted with run_claims dropped returned nil error, want a real (default-branch) error")
	}
	if errors.Is(err, storage.ErrClaimHeld) || errors.Is(err, storage.ErrClaimNotHeld) {
		t.Fatalf("sweepInterrupted error = %v, want a raw wrapped error, not one of the two handled sentinels", err)
	}
}

// TestSweepInterruptedPropagatesMarkInterruptedError covers
// sweepInterrupted's own "mark interrupted" interruptRunningRun error-wrap
// branch: a running row with no claim at all (the ErrClaimNotHeld
// branch, which does NOT skip like ErrClaimHeld) reaches the mark-
// interrupted call for real, which then fails against a trigger that
// makes every automation_runs UPDATE fail.
func TestSweepInterruptedPropagatesMarkInterruptedError(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()
	if err := svc.createRun(ctx, Run{
		ID: "run-sweep-mark-err", AutomationID: "auto-sweep-mark-err",
		State: RunRunning, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	// No admitFire call: no claim row exists, so TakeoverExpiredClaimFenced
	// hits ErrClaimNotHeld and the loop falls through to the mark-
	// interrupted call rather than skipping.
	if err := forceAutomationRunsUpdateFailures(t, db); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}
	if _, err := svc.sweepInterrupted(ctx, time.Hour); err == nil {
		t.Fatal("sweepInterrupted with automation_runs UPDATEs forced to fail: got nil error, want the mark-interrupted error wrapped")
	}
}

// TestSweepInterruptedPropagatesListRunningError covers sweepInterrupted's
// own ListRunningAutomationRuns error-wrap branch directly: dropping
// automation_runs (keeping run_claims intact) makes the very first store
// call inside sweepInterrupted fail, before the per-row loop is ever
// entered - distinct from TestSweepInterruptedPropagatesRealTakeoverError
// above, which drops run_claims instead and so fails one call later,
// inside the loop.
func TestSweepInterruptedPropagatesListRunningError(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("drop automation_runs table: %v", err)
	}
	if _, err := svc.sweepInterrupted(context.Background(), time.Minute); err == nil {
		t.Fatal("sweepInterrupted with automation_runs dropped: got nil error, want ListRunningAutomationRuns' own error")
	}
}

// TestGetRunPropagatesRealStoreError covers getRun's own
// GetAutomationRun error-wrap branch (distinct from TestGetRunNotFound,
// which reaches the store successfully and finds nothing): dropping
// automation_runs makes the underlying query itself fail with a real
// error rather than sql.ErrNoRows.
func TestGetRunPropagatesRealStoreError(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("drop automation_runs table: %v", err)
	}
	if _, _, err := svc.getRun(context.Background(), "any-id"); err == nil {
		t.Fatal("getRun with automation_runs dropped: got nil error, want a real store error")
	}
}

// TestUpdateRunSessionRoundTrip proves updateRunSession writes
// session_name for an existing run and getRun reflects it back.
func TestUpdateRunSessionRoundTrip(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	if err := svc.createRun(ctx, Run{ID: "run-sess", AutomationID: "auto-sess", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := svc.updateRunSession(ctx, "run-sess", "__auto__auto-sess__run-sess"); err != nil {
		t.Fatalf("updateRunSession: %v", err)
	}
	got, ok, err := svc.getRun(ctx, "run-sess")
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if got.SessionName != "__auto__auto-sess__run-sess" {
		t.Fatalf("SessionName = %q, want __auto__auto-sess__run-sess", got.SessionName)
	}
}

// TestUpdateRunSessionNilDBReturnsErrNoRunStore proves updateRunSession
// on a Service with db=nil returns errNoRunStore, matching every other
// runstore method's nil-db handling.
func TestUpdateRunSessionNilDBReturnsErrNoRunStore(t *testing.T) {
	svc := newTestService(t, nil)
	if err := svc.updateRunSession(context.Background(), "run-1", "some-session"); !errors.Is(err, errNoRunStore) {
		t.Fatalf("updateRunSession with nil db = %v, want errNoRunStore", err)
	}
}

// TestUpdateRunSessionUnknownRunReturnsError proves updateRunSession on
// an unknown run ID returns a wrapped error, not a silent success.
func TestUpdateRunSessionUnknownRunReturnsError(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	if err := svc.updateRunSession(context.Background(), "no-such-run", "some-session"); err == nil {
		t.Fatal("updateRunSession on missing run: got nil error, want rejection")
	}
}

// TestUpdateRunClaimTokenRoundTrip proves updateRunClaimToken writes
// claim_token for an existing run and getRun reflects it back.
func TestUpdateRunClaimTokenRoundTrip(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	if err := svc.createRun(ctx, Run{ID: "run-tok", AutomationID: "auto-tok", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := svc.updateRunClaimToken(ctx, "run-tok", "claim-token-abc"); err != nil {
		t.Fatalf("updateRunClaimToken: %v", err)
	}
	got, ok, err := svc.getRun(ctx, "run-tok")
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if got.ClaimToken != "claim-token-abc" {
		t.Fatalf("ClaimToken = %q, want claim-token-abc", got.ClaimToken)
	}
}

// TestUpdateRunClaimTokenNilDBReturnsErrNoRunStore proves
// updateRunClaimToken on a Service with db=nil returns errNoRunStore.
func TestUpdateRunClaimTokenNilDBReturnsErrNoRunStore(t *testing.T) {
	svc := newTestService(t, nil)
	if err := svc.updateRunClaimToken(context.Background(), "run-1", "some-token"); !errors.Is(err, errNoRunStore) {
		t.Fatalf("updateRunClaimToken with nil db = %v, want errNoRunStore", err)
	}
}

// TestUpdateRunClaimTokenUnknownRunReturnsError proves updateRunClaimToken
// on an unknown run ID returns a wrapped error, not a silent success.
func TestUpdateRunClaimTokenUnknownRunReturnsError(t *testing.T) {
	db := newTestDB(t)
	svc := newTestService(t, db)
	if err := svc.updateRunClaimToken(context.Background(), "no-such-run", "some-token"); err == nil {
		t.Fatal("updateRunClaimToken on missing run: got nil error, want rejection")
	}
}

// TestUpdateRunSessionPropagatesStoreError closes the store's underlying
// connection before calling updateRunSession, so
// UpdateAutomationRunSession fails with a real error - exercising
// updateRunSession's own error-wrap branch, distinct from the not-found
// case above (which reaches the store successfully and gets zero rows
// affected, not a real failure).
func TestUpdateRunSessionPropagatesStoreError(t *testing.T) {
	db := newTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	svc, err := New(t.TempDir(), db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.updateRunSession(context.Background(), "run-1", "some-session"); err == nil {
		t.Fatal("updateRunSession against a closed store returned nil error, want a real error")
	}
}

// TestUpdateRunClaimTokenPropagatesStoreError closes the store's
// underlying connection before calling updateRunClaimToken, so
// UpdateAutomationRunClaimToken fails with a real error - exercising
// updateRunClaimToken's own error-wrap branch.
func TestUpdateRunClaimTokenPropagatesStoreError(t *testing.T) {
	db := newTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	svc, err := New(t.TempDir(), db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.updateRunClaimToken(context.Background(), "run-1", "some-token"); err == nil {
		t.Fatal("updateRunClaimToken against a closed store returned nil error, want a real error")
	}
}
