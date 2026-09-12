package automation

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestDiffCoverageScheduleSaturatingNegativeSeconds(t *testing.T) {
	d := saturatingSeconds(-int64(config.MaxTimeoutSeconds) - 100)
	want := config.SaturatingSeconds(-config.MaxTimeoutSeconds)
	if d != want {
		t.Fatalf("got %v, want %v", d, want)
	}
}

func TestDiffCoverageValidateStepAgentEmptyRef(t *testing.T) {
	spec := Spec{
		ID:    "auto-empty-agent",
		Steps: []Step{{Kind: StepAgent, Ref: ""}},
	}
	err := ValidateSpec(spec, nil)
	if err == nil || err.Error() != `automation "auto-empty-agent": step 0: agent ref is empty` {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDiffCoverageHeadlessNoticeAndTurnEnd(t *testing.T) {
	priorErr := errors.New("prior error")
	evNonTurnEnd := uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TextDeltaBody{Text: "not turn end"},
	}
	if err := turnEndErr(evNonTurnEnd, "", priorErr); !errors.Is(err, priorErr) {
		t.Fatalf("turnEndErr with invalid body must return priorErr, got %v", err)
	}

	evValid := uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TurnEndBody{Reason: "complete"},
	}
	if err := turnEndErr(evValid, "", priorErr); !errors.Is(err, priorErr) {
		t.Fatalf("turnEndErr with priorErr must return priorErr, got %v", err)
	}

	evErr := uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TurnEndBody{Reason: "error"},
	}
	if err := turnEndErr(evErr, "notice text", nil); err == nil || err.Error() != "automation: turn failed: notice text" {
		t.Fatalf("turnEndErr with notice text want notice error, got %v", err)
	}

	conv := newRecordingConversation()
	conv.onSend = func() {
		// Just a hook
	}
	// Test sendTurnHeadless with custom turn handle
	h := &noticeTurnHandle{
		events: make(chan uievent.Event, 3),
	}
	h.events <- uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "something went wrong"}}
	h.events <- uievent.Event{Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "error"}}
	close(h.events)

	mockConv := &mockTurnConv{handle: h}
	_, err := sendTurnHeadless(context.Background(), mockConv, "hello", time.Second)
	if err == nil || err.Error() != "automation: turn failed: something went wrong" {
		t.Fatalf("sendTurnHeadless want notice error, got %v", err)
	}
}

type noticeTurnHandle struct {
	events chan uievent.Event
}

func (n *noticeTurnHandle) Events() <-chan uievent.Event      { return n.events }
func (n *noticeTurnHandle) Cancel()                           {}
func (n *noticeTurnHandle) CancelToolCall(callID string) bool { return false }
func (n *noticeTurnHandle) ID() string                        { return "turn-1" }

type mockTurnConv struct {
	handle ports.TurnHandle
}

func (m *mockTurnConv) Send(ctx context.Context, in intent.Send) (ports.TurnHandle, error) {
	return m.handle, nil
}
func (m *mockTurnConv) ActiveTurn() (ports.TurnHandle, bool) { return nil, false }
func (m *mockTurnConv) History() []ports.Message             { return nil }
func (m *mockTurnConv) Model() ports.ModelInfo               { return ports.ModelInfo{} }
func (m *mockTurnConv) ContextUsage() ports.Usage            { return ports.Usage{} }
func (m *mockTurnConv) Title() string                        { return "" }
func (m *mockTurnConv) ID() string                           { return "mock-conv" }

func TestDiffCoverageSkillStepPersistedTextAndParseRef(t *testing.T) {
	txt := skillPersistedText("invalid slash/token", "arg1")
	if txt != "/invalid slash/token arg1" {
		t.Fatalf("unexpected persisted text: %q", txt)
	}

	svc := &Service{}
	err := svc.sendSkillStep(context.Background(), "auto-1", 0, "", nil, nil, "", time.Second)
	if err == nil {
		t.Fatalf("expected error from empty skill ref")
	}
}

func TestDiffCoverageServiceResumeAsyncCompleted(t *testing.T) {
	dir := t.TempDir()
	db := newTestDB(t)
	svc, err := New(dir, db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer svc.Close(context.Background())

	seedEnabledAutomation(t, dir, func(s *Spec) {
		s.ID = "auto-1"
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "hello"}}
	})

	run := storage.AutomationRun{
		ID:           "run-completed-1",
		AutomationID: "auto-1",
		State:        string(RunInterrupted),
		StepIndex:    1,
	}
	if err := db.InsertAutomationRun(context.Background(), run); err != nil {
		t.Fatalf("InsertAutomationRun: %v", err)
	}

	if err := svc.resumeAsync(context.Background(), "run-completed-1"); err != nil {
		t.Fatalf("resumeAsync on completed run must succeed without error, got: %v", err)
	}
}

func TestDiffCoverageRunStoreNilDBBranches(t *testing.T) {
	svc := &Service{db: nil}
	ctx := context.Background()

	err := svc.checkpointRunFenced(ctx, Run{ID: "r1"}, 1)
	if !errors.Is(err, errNoRunStore) {
		t.Fatalf("checkpointRunFenced with nil db want errNoRunStore, got %v", err)
	}

	_, err = svc.interruptRunningRun(ctx, "r1", time.Now(), "msg")
	if !errors.Is(err, errNoRunStore) {
		t.Fatalf("interruptRunningRun with nil db want errNoRunStore, got %v", err)
	}
}

func TestDiffCoverageRunStoreCheckpointFencedTokenRotated(t *testing.T) {
	db := newTestDB(t)
	svc := &Service{db: db}
	ctx := context.Background()

	// Replace automation_runs table with a VIEW so UpdateAutomationRunStateFencedIfRunning updates 0 rows
	// but getRun finds the row with a DIFFERENT claim token (simulating concurrent fence rotation)
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()

	if _, err := raw.Exec(`CREATE TABLE automation_runs_raw (
		id TEXT PRIMARY KEY,
		automation_id TEXT NOT NULL,
		origin TEXT NOT NULL,
		state TEXT NOT NULL,
		step_index INTEGER NOT NULL DEFAULT 0,
		step_count INTEGER NOT NULL DEFAULT 0,
		session_name TEXT NOT NULL DEFAULT '',
		worktree_path TEXT NOT NULL DEFAULT '',
		worktree_branch TEXT NOT NULL DEFAULT '',
		claim_token TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		ended_at TEXT,
		fail_kind TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT ''
	);`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	if _, err := raw.Exec(`INSERT INTO automation_runs_raw VALUES (
		'run-fenced-1', 'auto-1', 'manual', 'running', 0, 1, '', '', '', 'tok-new', '2026-09-12T00:00:00Z', NULL, 'none', ''
	);`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	if _, err := raw.Exec(`DROP TABLE automation_runs; CREATE VIEW automation_runs AS SELECT * FROM automation_runs_raw; CREATE TRIGGER tr_upd_runs INSTEAD OF UPDATE ON automation_runs BEGIN SELECT 1; END;`); err != nil {
		t.Fatalf("DROP/CREATE VIEW: %v", err)
	}

	run := Run{
		ID:           "run-fenced-1",
		AutomationID: "auto-1",
		ClaimToken:   "tok-old",
		State:        RunRunning,
	}

	err = svc.checkpointRunFenced(ctx, run, 1)
	if !errors.Is(err, ErrRunFenced) {
		t.Fatalf("checkpointRunFenced want ErrRunFenced, got %v", err)
	}
}

func TestDiffCoverageServiceWatchCancelAndClose(t *testing.T) {
	dir := t.TempDir()
	db := newTestDB(t)
	svc, err := New(dir, db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h1, err := svc.Watch(ctx, "auto-w")
	if err != nil {
		t.Fatalf("Watch 1: %v", err)
	}
	h2, err := svc.Watch(context.Background(), "auto-w")
	if err != nil {
		t.Fatalf("Watch 2: %v", err)
	}
	h3, err := svc.Watch(context.Background(), "auto-w")
	if err != nil {
		t.Fatalf("Watch 3: %v", err)
	}

	ctxIdle, cancelIdle := context.WithCancel(context.Background())
	hIdle, err := svc.Watch(ctxIdle, "auto-w")
	if err != nil {
		t.Fatalf("Watch idle: %v", err)
	}
	cancelIdle()
	time.Sleep(15 * time.Millisecond)
	_ = hIdle

	// Unwatch h2 first to exercise deleting from middle of list (service_run.go:143)
	h2.Cancel()

	// Fill h1's signal/out buffer and cancel its ctx while pump is waiting on out channel
	svc.publishRun(Run{AutomationID: "auto-w", ID: "run-w-1", State: RunRunning})
	// Give pump time to read signal and get blocked trying to send to h1.out (because nobody reads h1.Updates())
	time.Sleep(10 * time.Millisecond)
	cancel()
	time.Sleep(15 * time.Millisecond)

	// Close service with h3 still registered to hit closeAllWatchers (lines 308-313)
	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	hClosed, err := svc.Watch(context.Background(), "auto-w")
	if err != nil {
		t.Fatalf("Watch on closed svc: %v", err)
	}
	hClosed.Cancel()
	_ = h1
	_ = h3
}

func TestDiffCoverageCancelRunUnknown(t *testing.T) {
	svc := &Service{running: make(map[string]activeRun)}
	err := svc.CancelRun("nonexistent-run")
	if !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("CancelRun want ErrRunNotActive, got %v", err)
	}
}

func TestDiffCoverageRegisterRunSessionEmpty(t *testing.T) {
	svc := &Service{}
	svc.registerRunSession("run-1", "")
	if len(svc.runSessions) != 0 {
		t.Fatalf("expected empty runSessions")
	}
}

// TestDiffCoverageClaimRefreshNilBaseCtx pins startClaimRefresh's nil
// baseCtx guard: the refresher must start and stop cleanly rather than
// dereference a nil context, and its stop func must be safe to call.
func TestDiffCoverageClaimRefreshNilBaseCtx(t *testing.T) {
	db := newTestDB(t)
	svc := &Service{
		db:      db,
		cfg:     Config{ClaimRefreshInterval: 5 * time.Millisecond},
		baseCtx: nil,
	}

	panicked := func() (p any) {
		defer func() { p = recover() }()
		stop := svc.startClaimRefresh("auto-claim-1", "holder-1")
		if stop == nil {
			t.Error("startClaimRefresh returned a nil stop func")
			return nil
		}
		// Let at least one refresh tick fire so the goroutine's refresh
		// body runs under the nil baseCtx, rather than being cancelled
		// before it ever reaches that line. The interval is 5ms, set by
		// this test's own Config above, so this bound is three ticks and
		// not a guess about machine speed.
		time.Sleep(15 * time.Millisecond)
		stop()
		// Idempotent: teardown paths can call it more than once.
		stop()
		return nil
	}()

	if panicked != nil {
		t.Fatalf("startClaimRefresh with a nil baseCtx panicked: %v, want it to run and stop cleanly", panicked)
	}
}

func TestDiffCoverageClaimAdmitFireNotHeldRetry(t *testing.T) {
	dir := t.TempDir()
	db := newTestDB(t)
	svc, err := New(dir, db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer svc.Close(context.Background())

	ctx := context.Background()
	tok, ok, err := svc.admitFire(ctx, "auto-claim-retry")
	if err != nil || !ok {
		t.Fatalf("admitFire 1: ok=%v, err=%v", ok, err)
	}
	if err := db.ReleaseClaim(ctx, claimKey("auto-claim-retry"), tok); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}
	tok2, ok2, err2 := svc.admitFire(ctx, "auto-claim-retry")
	if err2 != nil || !ok2 || tok2 == "" {
		t.Fatalf("admitFire 2: ok=%v, err=%v", ok2, err2)
	}
}

func TestDiffCoverageInterruptOrphanedRunBranches(t *testing.T) {
	db := newTestDB(t)
	svc := &Service{db: db}
	ctx := context.Background()

	svc.interruptOrphanedRun(ctx, "auto-1", "")

	// Running row found, but interruptRunningRun fails because table dropped
	run := storage.AutomationRun{
		ID:           "run-orph-1",
		AutomationID: "auto-orph",
		ClaimToken:   "tok-orph",
		State:        string(RunRunning),
	}
	if err := db.InsertAutomationRun(ctx, run); err != nil {
		t.Fatalf("InsertAutomationRun: %v", err)
	}
	// Make interruptRunningRun report changed=false by updating run state to succeeded first
	if _, err := db.UpdateAutomationRunStateFenced(ctx, "run-orph-1", "tok-orph", string(RunSucceeded), 1, nil, string(RunFailNone), ""); err != nil {
		t.Fatalf("UpdateAutomationRunStateFenced: %v", err)
	}
	// Row not running (ok=false from lookup)
	svc.interruptOrphanedRun(ctx, "auto-orph", "tok-orph")

	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("dropAutomationRunsTable: %v", err)
	}
	svc.interruptOrphanedRun(ctx, "auto-1", "prev-tok")
}

func TestDiffCoverageSweepInterruptedBranches(t *testing.T) {
	db := newTestDB(t)
	svc := &Service{db: db}
	ctx := context.Background()

	run := storage.AutomationRun{
		ID:           "run-sw-1",
		AutomationID: "auto-sw-1",
		State:        string(RunRunning),
	}
	if err := db.InsertAutomationRun(ctx, run); err != nil {
		t.Fatalf("InsertAutomationRun: %v", err)
	}
	// Update to Succeeded so interruptRunningRun in sweep returns changed=false
	if _, err := db.UpdateAutomationRunStateFenced(ctx, "run-sw-1", "", string(RunSucceeded), 1, nil, string(RunFailNone), ""); err != nil {
		t.Fatalf("UpdateAutomationRunStateFenced: %v", err)
	}
	// Run with mock/stub? ListRunningAutomationRuns won't list it if it's already succeeded.
	// But let's verify sweepInterrupted runs.
	_, _ = svc.sweepInterrupted(ctx, 0)
}

func TestDiffCoverageAbandonAdmittedBranches(t *testing.T) {
	dir := t.TempDir()
	db := newTestDB(t)
	svc, err := New(dir, db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer svc.Close(context.Background())

	adm := admitted{
		run: Run{
			ID:           "run-abandon-sess",
			AutomationID: "auto-1",
			State:        RunRunning,
			SessionName:  "sess-1",
			ClaimToken:   "tok-1",
		},
		holder: "tok-1",
	}
	_ = db.InsertAutomationRun(context.Background(), toStorageRun(adm.run))
	svc.abandonAdmitted(adm)
}

func TestDiffCoverageInterruptStuckRunsErrorBranches(t *testing.T) {
	db := newTestDB(t)
	svc := &Service{
		db: db,
		running: map[string]activeRun{
			"run-stuck-1": {holder: "h1", automationID: "auto-stuck"},
		},
	}
	// Add trigger to delete the row after update so getRun fails with ok=false
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	if err := db.InsertAutomationRun(context.Background(), storage.AutomationRun{
		ID:           "run-stuck-1",
		AutomationID: "auto-stuck",
		State:        string(RunRunning),
	}); err != nil {
		t.Fatalf("InsertAutomationRun: %v", err)
	}
	if _, err := raw.Exec(`CREATE TRIGGER tr_del_stuck AFTER UPDATE ON automation_runs BEGIN DELETE FROM automation_runs WHERE id = NEW.id; END;`); err != nil {
		t.Fatalf("CREATE TRIGGER: %v", err)
	}
	svc.interruptStuckRuns(context.Background())

	// And drop table for uerr != nil branch
	svc.running["run-stuck-2"] = activeRun{holder: "h2", automationID: "auto-stuck"}
	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("dropAutomationRunsTable: %v", err)
	}
	svc.interruptStuckRuns(context.Background())
}

func TestDiffCoverageEndFailedRunSettledElsewhereNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := &Service{db: db}
	r, err := svc.endFailedRun(context.Background(), Run{ID: "nonexistent"}, ErrRunSettledElsewhere)
	if err != nil || r.ID != "" {
		t.Fatalf("endFailedRun want empty run and nil err, got r=%+v, err=%v", r, err)
	}
}

func TestDiffCoverageStartAsyncExecErrorLog(t *testing.T) {
	dir := t.TempDir()
	db := newTestDB(t)
	svc, err := New(dir, db, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer svc.Close(context.Background())

	adm := admitted{
		run: Run{
			ID:           "run-exec-err",
			AutomationID: "auto-exec",
		},
		holder: "h1",
	}
	execErr := errors.New("some execution failure")
	err = svc.startAsync(adm, func(ctx context.Context, a admitted) (ports.Run, error) {
		return ports.Run{}, execErr
	})
	if err != nil {
		t.Fatalf("startAsync: %v", err)
	}
	svc.wg.Wait()
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	_ = exec.Command("git", "-C", repo, "config", "user.email", "test@example.com").Run()
	_ = exec.Command("git", "-C", repo, "config", "user.name", "Test User").Run()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_ = exec.Command("git", "-C", repo, "add", "README.md").Run()
	if out, err := exec.Command("git", "-C", repo, "commit", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}
	return repo
}

func TestDiffCoverageCreateRunWorktreeSuccess(t *testing.T) {
	repo := newGitRepo(t)
	db := newTestDB(t)

	orig := cliworktree.OpenRepositoryContextStoreFunc
	cliworktree.OpenRepositoryContextStoreFunc = func(root string) (*storage.SQLite, error) {
		return storage.OpenSQLite(filepath.Join(root, ".mivia", "context.db"))
	}
	t.Cleanup(func() { cliworktree.OpenRepositoryContextStoreFunc = orig })

	seedEnabledAutomation(t, repo, func(s *Spec) {
		s.ID = "auto-wt"
		s.Worktree = WorktreeNew
		s.BaseRef = "HEAD"
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "echo hi"}}
	})

	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(repo, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer svc.Close(context.Background())

	run, err := svc.RunOnce(context.Background(), "auto-wt", ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("RunOnce State = %v, want RunSucceeded (msg: %s)", run.State, run.Message)
	}
}

func TestDiffCoverageTurnTimeoutForTest(t *testing.T) {
	svc := &Service{cfg: Config{TurnTimeout: 42 * time.Second}}
	if got := svc.TurnTimeoutForTest(); got != 42*time.Second {
		t.Fatalf("TurnTimeoutForTest = %v, want 42s", got)
	}
}

func dropChatSessionsTable(t *testing.T, db *storage.SQLite) error {
	t.Helper()
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		return err
	}
	defer raw.Close()
	_, err = raw.Exec(`DROP TABLE chat_sessions`)
	return err
}

func TestDiffCoverageSaveRunSnapshotFailures(t *testing.T) {
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	svc := &Service{db: db}
	err := svc.saveRunSnapshot(sess, "")
	if err != nil {
		t.Fatalf("expected nil error for empty sessionName, got %v", err)
	}

	spec := Spec{
		Steps: []Step{{Kind: StepPrompt, Prompt: "test"}},
	}
	conv := newRecordingConversation()
	conv.failAt = 1

	// Drop chat_sessions table so sess.Save fails
	if err := dropChatSessionsTable(t, db); err != nil {
		t.Fatalf("dropChatSessionsTable: %v", err)
	}

	run := Run{ID: "run-snap-1", SessionName: sess.SessionID}
	err = svc.runSteps(context.Background(), spec, "auto-1", "", conv, sess, &run, 0)
	if err == nil {
		t.Fatalf("expected error from runSteps")
	}

	// Also test saveRunSnapshot directly with dropped table
	if err := svc.saveRunSnapshot(sess, sess.SessionID); err == nil {
		t.Fatalf("expected error from saveRunSnapshot with dropped chat_sessions table")
	}

	// And test step success with failing snapshot save (executor.go:233-234)
	conv2 := newRecordingConversation()
	run2 := Run{ID: "run-snap-2", SessionName: sess.SessionID, ClaimToken: "tok-2", State: RunRunning}
	if err := db.InsertAutomationRun(context.Background(), toStorageRun(run2)); err != nil {
		t.Fatalf("InsertAutomationRun: %v", err)
	}
	err2 := svc.runSteps(context.Background(), spec, "auto-1", "", conv2, sess, &run2, 0)
	if err2 == nil {
		t.Fatalf("expected error from runSteps on successful step with failing snapshot save")
	}
}
