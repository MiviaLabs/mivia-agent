package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// stubCompleter is resume_test.go's own minimal provider.Completer: every
// test in this file drives step dispatch through recordingConversation
// directly (StepPrompt/StepSkill/StepSlash never touch the underlying
// *chat.Session's own Completer), so this stub is never actually
// invoked - it exists purely so chat.NewSession has a non-nil,
// interface-satisfying value to hold.
type stubCompleter struct{}

func (stubCompleter) Name() string { return "stub" }
func (stubCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return "", nil
}
func (stubCompleter) Chat(ctx context.Context, req provider.Request) (string, error) { return "", nil }
func (stubCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	return &provider.Response{}, nil
}

// newContextEnabledSession builds a *chat.Session wired to store exactly
// the way this package's own executor_test.go peers in internal/chat
// (e.g. persistence_all_test.go, context_storage_test.go) do: a bound
// Principal, an enabled ContextManager, and store installed via
// SetContextStore - so sess.ContextEnabled() is true and sess.Save/Load
// round-trip through store for real.
func newContextEnabledSession(t *testing.T, store contextstate.Store) *chat.Session {
	t.Helper()
	sess := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, stubCompleter{})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatalf("SetContextManager: %v", err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatalf("SetContextStore: %v", err)
	}
	return sess
}

// sessionSpawner is resume_test.go's own SessionSpawner double: unlike
// executor_test.go's fakeExecSpawner (which always binds a nil
// *chat.Session), it hands CreateFreshInDir's bind closure a real,
// context-enabled *chat.Session, so ResumeRun's own boundSess.Load call
// (and spawnRunSession's own boundSess.Save call) have something real to
// operate on. insideBind is set for the exact duration of the bind
// closure's own call, letting a wrapped contextstate.Store detect
// whether a durable call happened WHILE bind was still running (the
// ordering spawnRunSession/spawnResumeSession must never violate).
type sessionSpawner struct {
	mu             sync.Mutex
	sess           *chat.Session
	conv           *recordingConversation
	insideBind     int32
	createCalls    int
	createErr      error
	setApprovalErr error
	overrides      []approvalOverride
}

func (f *sessionSpawner) CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
	f.mu.Lock()
	f.createCalls++
	createErr := f.createErr
	f.mu.Unlock()
	if createErr != nil {
		return nil, createErr
	}
	atomic.StoreInt32(&f.insideBind, 1)
	var bindErr error
	if bind != nil {
		_, bindErr = bind(f.sess)
	}
	atomic.StoreInt32(&f.insideBind, 0)
	if bindErr != nil {
		return nil, bindErr
	}
	return f.conv, nil
}

func (f *sessionSpawner) SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setApprovalErr != nil {
		return f.setApprovalErr
	}
	f.overrides = append(f.overrides, approvalOverride{sessionID: sessionID, gate: gate, policy: policy})
	return nil
}

func (f *sessionSpawner) createCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCalls
}

// instrumentedStore wraps a real *storage.SQLite, delegating every
// contextstate.Store/SessionCatalog method to it except SaveSession,
// which it intercepts to record whether the call happened while
// insideBind (a *sessionSpawner's own flag) was set - the precise
// ordering violation TestSpawnRunSessionSaveOrderingHappensAfterCreateFreshInDir
// exists to rule out.
type instrumentedStore struct {
	*storage.SQLite
	insideBind *int32

	mu             sync.Mutex
	saveCalls      int
	saveDuringBind bool
	saveErr        error
}

func (i *instrumentedStore) SaveSession(ctx context.Context, principal contextstate.Principal, name string, data []byte, model, providerName string, turns, tokens, msgCount int, opts contextstate.SessionSaveOptions) error {
	i.mu.Lock()
	i.saveCalls++
	if atomic.LoadInt32(i.insideBind) == 1 {
		i.saveDuringBind = true
	}
	err := i.saveErr
	i.mu.Unlock()
	if err != nil {
		return err
	}
	return i.SQLite.SaveSession(ctx, principal, name, data, model, providerName, turns, tokens, msgCount, opts)
}

// TestSpawnRunSessionSaveOrderingHappensAfterCreateFreshInDir proves
// spawnRunSession's own Save call happens strictly after
// CreateFreshInDir returns (and after SetApprovalOverride), never
// inside the bind closure itself - the same ordering
// uiadapter.SetApprovalOverride's own doc comment already requires for
// the approval override.
func TestSpawnRunSessionSaveOrderingHappensAfterCreateFreshInDir(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	store := &instrumentedStore{SQLite: db, insideBind: &spawner.insideBind}
	sess := newContextEnabledSession(t, store)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conv, boundSess, savedName, err := svc.spawnRunSession(Spec{ID: "auto-x"}, "auto-x", "run-x", root)
	if err != nil {
		t.Fatalf("spawnRunSession: %v", err)
	}
	if conv == nil {
		t.Fatal("spawnRunSession returned a nil conversation")
	}
	if boundSess != sess {
		t.Fatal("spawnRunSession did not return the bind closure's captured session")
	}
	want := automationSessionName("auto-x", "run-x")
	if savedName != want {
		t.Fatalf("savedName = %q, want %q", savedName, want)
	}
	if store.saveDuringBind {
		t.Fatal("Save happened while CreateFreshInDir's bind closure was still running, want it strictly after CreateFreshInDir returns")
	}
	if store.saveCalls != 1 {
		t.Fatalf("SaveSession called %d times, want exactly 1", store.saveCalls)
	}
}

// TestSpawnRunSessionSaveErrorFailsRun proves a Save failure is now a
// HARD error (not the package's earlier best-effort swallow): the
// caller sees it and every return value is zeroed.
func TestSpawnRunSessionSaveErrorFailsRun(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	store := &instrumentedStore{SQLite: db, insideBind: &spawner.insideBind, saveErr: fmt.Errorf("boom-save")}
	sess := newContextEnabledSession(t, store)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conv, boundSess, savedName, err := svc.spawnRunSession(Spec{ID: "auto-x"}, "auto-x", "run-x", root)
	if err == nil {
		t.Fatal("spawnRunSession with a failing Save: got nil error, want it propagated")
	}
	if !strings.Contains(err.Error(), "save run session") || !strings.Contains(err.Error(), "boom-save") {
		t.Fatalf("spawnRunSession error = %q, want it naming both the save-run-session wrap and the underlying boom-save cause", err.Error())
	}
	if conv != nil || boundSess != nil || savedName != "" {
		t.Fatalf("spawnRunSession with a failing Save returned (%v, %v, %q), want all zero", conv, boundSess, savedName)
	}
}

// seedResumableRun creates a Spec of the given prompts plus a run row
// carrying state/stepIndex, and returns (automationID, runID,
// sessionName). When sessionName != "", it also Saves sess under that
// name, standing in for a prior real spawnRunSession's own Save call -
// the snapshot ResumeRun's own boundSess.Load must find.
func seedResumableRun(t *testing.T, svc *Service, root string, sess *chat.Session, prompts []string, state RunState, stepIndex int, withSession bool) (automationID, runID, sessionName string) {
	t.Helper()
	steps := make([]Step, 0, len(prompts))
	for _, p := range prompts {
		steps = append(steps, Step{Kind: StepPrompt, Prompt: p})
	}
	automationID = seedEnabledAutomation(t, root, func(s *Spec) { s.Steps = steps })
	runID = "resume-run-" + automationID
	if withSession {
		sessionName = automationSessionName(automationID, runID)
		if err := sess.Save(sessionName); err != nil {
			t.Fatalf("pre-save resumable session: %v", err)
		}
	}
	run := Run{
		ID:           runID,
		AutomationID: automationID,
		Origin:       "manual",
		State:        state,
		StepIndex:    stepIndex,
		StepCount:    len(steps),
		SessionName:  sessionName,
	}
	if err := svc.createRun(context.Background(), run); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	return automationID, runID, sessionName
}

// TestResumeRunStartsAtStepIndexWithoutAdjustment covers ResumeRun's own
// no-off-by-one contract: a run interrupted with StepIndex=1 of 3 steps
// resumes at steps[1] and steps[2] - never steps[0] (already done) and
// never just steps[2] (that would silently skip a step ResumeRun's own
// checkpoint semantics say was never completed).
func TestResumeRunStartsAtStepIndexWithoutAdjustment(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	sess := newContextEnabledSession(t, db)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one", "two", "three"}, RunInterrupted, 1, true)

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("ResumeRun State = %v, want RunSucceeded", run.State)
	}
	got := spawner.conv.sentTexts()
	want := []string{"two", "three"}
	if len(got) != len(want) {
		t.Fatalf("sent texts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sent texts = %v, want %v", got, want)
		}
	}
}

// TestResumeRunAfterFailureRerunsFailingStep covers the failure-
// checkpoint case: a run that FAILED at StepIndex=2 (of 3 steps) - where
// StepIndex names the step that itself failed, per runSteps' own
// contract - resumes by re-running that exact step whole, not skipping
// past it.
func TestResumeRunAfterFailureRerunsFailingStep(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	sess := newContextEnabledSession(t, db)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one", "two", "three"}, RunFailed, 2, true)

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("ResumeRun State = %v, want RunSucceeded", run.State)
	}
	got := spawner.conv.sentTexts()
	if len(got) != 1 || got[0] != "three" {
		t.Fatalf("sent texts = %v, want [three] (the failing step re-run whole)", got)
	}
}

// TestResumeRunUnknownRunReturnsErrRunNotFound proves an unknown run id
// is rejected by name.
func TestResumeRunUnknownRunReturnsErrRunNotFound(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &sessionSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.ResumeRun(context.Background(), "no-such-run"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("ResumeRun(unknown): err = %v, want ErrRunNotFound", err)
	}
}

// TestResumeRunSucceededRunReturnsErrRunNotResumable proves the state
// gate: only RunInterrupted/RunFailed are resumable.
func TestResumeRunSucceededRunReturnsErrRunNotResumable(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	sess := newContextEnabledSession(t, db)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one"}, RunSucceeded, 1, false)

	if _, err := svc.ResumeRun(context.Background(), runID); !errors.Is(err, ErrRunNotResumable) {
		t.Fatalf("ResumeRun(succeeded run): err = %v, want ErrRunNotResumable", err)
	}
	if spawner.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0", spawner.createCallCount())
	}
}

// TestResumeRunMissingSessionNameReturnsErrRunSessionMissing proves the
// no-SessionName guard fires BEFORE any spawn attempt (fail fast, no
// wasted session spawn).
func TestResumeRunMissingSessionNameReturnsErrRunSessionMissing(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	sess := newContextEnabledSession(t, db)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one"}, RunInterrupted, 0, false)

	if _, err := svc.ResumeRun(context.Background(), runID); !errors.Is(err, ErrRunSessionMissing) {
		t.Fatalf("ResumeRun(no session name): err = %v, want ErrRunSessionMissing", err)
	}
	if spawner.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0 (must fail before any spawn attempt)", spawner.createCallCount())
	}
}

// TestResumeRunHeldClaimReturnsErrRunAlreadyActive proves ResumeRun's
// own lost-claim behavior is a NAMED refusal, not RunOnce's silent
// RunSkipped no-op: Resume is user-initiated and must surface the
// conflict.
func TestResumeRunHeldClaimReturnsErrRunAlreadyActive(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	sess := newContextEnabledSession(t, db)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	automationID, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one", "two"}, RunInterrupted, 0, true)

	if _, ok, err := svc.admitFire(context.Background(), automationID); err != nil || !ok {
		t.Fatalf("pre-acquire admitFire: ok=%v err=%v", ok, err)
	}

	if _, err := svc.ResumeRun(context.Background(), runID); !errors.Is(err, ErrRunAlreadyActive) {
		t.Fatalf("ResumeRun(held claim): err = %v, want ErrRunAlreadyActive", err)
	}
	if spawner.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0 (a refused resume must have zero side effects)", spawner.createCallCount())
	}
	// The original row must be untouched - no RunSkipped row was
	// fabricated, and the interrupted row itself did not change state.
	got, ok, err := svc.getRun(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("getRun after refused resume: ok=%v err=%v", ok, err)
	}
	if got.State != RunInterrupted {
		t.Fatalf("run state after refused resume = %v, want unchanged RunInterrupted", got.State)
	}
}

// TestResumeRunReleasesClaimOnSuccess proves the claim ResumeRun takes
// via admitFire is released once the resumed run completes
// successfully - a later admitFire for the same automation succeeds.
func TestResumeRunReleasesClaimOnSuccess(t *testing.T) {
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

	if _, err := svc.ResumeRun(context.Background(), runID); err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if _, ok, err := svc.admitFire(context.Background(), automationID); err != nil || !ok {
		t.Fatalf("admitFire after successful resume: ok=%v err=%v, want ok=true (claim released)", ok, err)
	}
}

// TestResumeRunReleasesClaimOnFailure proves the same release happens
// even when the resumed run itself fails.
func TestResumeRunReleasesClaimOnFailure(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	conv := newRecordingConversation()
	conv.failAt = 1
	spawner := &sessionSpawner{conv: conv}
	sess := newContextEnabledSession(t, db)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	automationID, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one"}, RunInterrupted, 0, true)

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: got error %v, want nil (failure is recorded on the run)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("ResumeRun State = %v, want RunFailed", run.State)
	}
	if _, ok, err := svc.admitFire(context.Background(), automationID); err != nil || !ok {
		t.Fatalf("admitFire after failed resume: ok=%v err=%v, want ok=true (claim released)", ok, err)
	}
}

// TestResumeRunAlreadyCompleteMarksSucceededWithoutSpawn covers the
// StepIndex >= len(spec.Steps) branch: a race or stale interrupted flag
// on an already-fully-executed run is repaired to RunSucceeded without
// ever spawning a session or touching the fenced claim.
func TestResumeRunAlreadyCompleteMarksSucceededWithoutSpawn(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	sess := newContextEnabledSession(t, db)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one", "two"}, RunInterrupted, 2, true)

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("ResumeRun State = %v, want RunSucceeded", run.State)
	}
	if spawner.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0 (already-complete run needs no spawn)", spawner.createCallCount())
	}
}

// TestResumeRunMissingWorktreeFailsRun proves ResumeRun's own worktree
// existence check: a run whose WorktreePath no longer exists on disk
// fails the resume rather than silently falling back to root.
func TestResumeRunMissingWorktreeFailsRun(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawner := &sessionSpawner{conv: newRecordingConversation()}
	sess := newContextEnabledSession(t, db)
	spawner.sess = sess
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	automationID, runID, sessionName := seedResumableRun(t, svc, root, sess, []string{"one"}, RunInterrupted, 0, true)
	if err := svc.updateRunSession(context.Background(), runID, sessionName); err != nil {
		t.Fatalf("updateRunSession: %v", err)
	}
	_ = automationID

	// Stamp a worktree path that never existed on disk. Directly mutate
	// the row via a second raw SQL connection, since runstore.go exposes
	// no updateRunWorktree helper - the same raw-SQL escape hatch
	// runstore_test.go's own drop/force helpers use.
	if err := stampRunWorktreePath(t, db, runID, root+"/nonexistent-worktree"); err != nil {
		t.Fatalf("stamp worktree path: %v", err)
	}

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: got error %v, want nil (worktree failure is recorded on the run)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("ResumeRun State = %v, want RunFailed", run.State)
	}
	if spawner.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0 (missing worktree must precede session spawn)", spawner.createCallCount())
	}
}

// stampRunWorktreePath opens a second, independent connection to db's
// underlying SQLite file and sets one run's worktree_path directly -
// runstore.go exposes no such write path (a run's worktree is only ever
// set at spawn time, not mutated later), so this is the only way a test
// can put a run row into the "worktree path recorded but missing on
// disk" state ResumeRun's own os.Stat guard exists to catch.
func stampRunWorktreePath(t *testing.T, db *storage.SQLite, runID, path string) error {
	t.Helper()
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		return err
	}
	defer raw.Close()
	_, err = raw.Exec(`UPDATE automation_runs SET worktree_path = ? WHERE id = ?`, path, runID)
	return err
}

// TestSweepInterruptedExportedWrapper proves SweepInterrupted delegates
// to sweepInterrupted with the package's own default staleness
// threshold: a RunRunning row with no claim at all is flagged
// regardless of that default (the no-claim branch does not depend on
// maxAge), proving the exported wrapper is wired through for real.
func TestSweepInterruptedExportedWrapper(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &sessionSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if n, err := svc.SweepInterrupted(context.Background()); err != nil || n != 0 {
		t.Fatalf("SweepInterrupted on empty store = (%d, %v), want (0, nil)", n, err)
	}

	if err := svc.createRun(context.Background(), Run{
		ID: "sweep-wrapper-run", AutomationID: "sweep-wrapper-auto", State: RunRunning,
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	n, err := svc.SweepInterrupted(context.Background())
	if err != nil {
		t.Fatalf("SweepInterrupted: %v", err)
	}
	if n != 1 {
		t.Fatalf("SweepInterrupted returned %d, want 1", n)
	}
	got, ok, err := svc.getRun(context.Background(), "sweep-wrapper-run")
	if err != nil || !ok {
		t.Fatalf("getRun after sweep: ok=%v err=%v", ok, err)
	}
	if got.State != RunInterrupted {
		t.Fatalf("state after SweepInterrupted = %v, want RunInterrupted", got.State)
	}
}
