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
	"time"

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
	// resumeSess, when set, is what GetOrResumeInDir returns instead of
	// sess: a session whose own id differs from the run's stored
	// SessionName (the re-point fixture). getOrResumeErr, when set, is
	// returned by GetOrResumeInDir directly. getOrResumeIDs records
	// every id GetOrResumeInDir was asked for, in call order.
	resumeSess       *chat.Session
	getOrResumeErr   error
	getOrResumeCalls int
	getOrResumeIDs   []string
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

// GetOrResumeInDir stands in for a real pool resume: it counts as the
// create+load pair it replaces (so createCallCount assertions stay
// meaningful), returns the same bound session CreateFreshInDir hands
// out, and - when this fake has a real session - restores history with
// Load(id) itself, since the caller must not call Load again. A failed
// Load is wrapped the way a real spawner wraps it (cliautomations'
// "cliautomations: load session %q" wrap); no real spawner attaches
// ErrRunSessionMissing - that refusal is the caller's own admission
// guard for a run with no recorded name at all. Every requested id is
// recorded, so a test can pin which name a resume goes back through.
func (f *sessionSpawner) GetOrResumeInDir(id string, dir string) (ports.Conversation, *chat.Session, error) {
	f.mu.Lock()
	f.createCalls++
	f.getOrResumeCalls++
	f.getOrResumeIDs = append(f.getOrResumeIDs, id)
	createErr, resumeErr := f.createErr, f.getOrResumeErr
	f.mu.Unlock()
	if resumeErr != nil {
		return nil, nil, resumeErr
	}
	if createErr != nil {
		return nil, nil, createErr
	}
	if f.resumeSess != nil {
		// Fresh-ID mode: the saved name resolved onto a session whose own
		// id differs; that fixture session already carries its state, so
		// no Load happens here.
		return f.conv, f.resumeSess, nil
	}
	if f.sess != nil {
		if err := f.sess.Load(id); err != nil {
			return nil, nil, fmt.Errorf("cliautomations: load session %q: %w", id, err)
		}
	}
	return f.conv, f.sess, nil
}

// getOrResumeRequestedIDs returns every id passed to GetOrResumeInDir,
// in call order.
func (f *sessionSpawner) getOrResumeRequestedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.getOrResumeIDs))
	copy(out, f.getOrResumeIDs)
	return out
}

func (f *sessionSpawner) getOrResumeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getOrResumeCalls
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

	mu               sync.Mutex
	saveCalls        int
	saveDuringBind   bool
	saveErr          error
	loadSessionCalls int
}

// LoadSession counts catalog loads so a test can tell a spawner's own
// history restore from any extra chat Load the caller was told not to
// make.
func (i *instrumentedStore) LoadSession(ctx context.Context, principal contextstate.Principal, name string) ([]byte, contextstate.SessionCatalogInfo, error) {
	i.mu.Lock()
	i.loadSessionCalls++
	i.mu.Unlock()
	return i.SQLite.LoadSession(ctx, principal, name)
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

	conv, boundSess, savedName, err := svc.spawnRunSession(Spec{ID: "auto-x"}, "auto-x", root)
	if err != nil {
		t.Fatalf("spawnRunSession: %v", err)
	}
	if conv == nil {
		t.Fatal("spawnRunSession returned a nil conversation")
	}
	if boundSess != sess {
		t.Fatal("spawnRunSession did not return the bind closure's captured session")
	}
	want := sess.SessionID
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

	conv, boundSess, savedName, err := svc.spawnRunSession(Spec{ID: "auto-x"}, "auto-x", root)
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
		// The one-catalog-row scheme: a run's snapshot lives under the
		// bound session's own id.
		sessionName = sess.SessionID
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

// TestResumeRunReSnapshotsSessionAfterEachCheckpoint proves a RESUMED
// run's own per-step checkpoints keep re-saving the session snapshot
// too, not just the pre-resume Load's own transcript: after a two-step
// resume completes, the catalog row under run.SessionName carries both
// steps' messages, so a SECOND interruption (after only the first
// resumed step) would not lose that step's progress - runSteps' own
// snapshot save is not special-cased away on the resume path.
func TestResumeRunReSnapshotsSessionAfterEachCheckpoint(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	conv := newRecordingConversation()
	conv.onSend = func() {
		if _, err := sess.SendUser(context.Background(), "resumed step turn", io.Discard); err != nil {
			t.Errorf("SendUser during resumed step: %v", err)
		}
	}
	spawner := &sessionSpawner{sess: sess, conv: conv}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, sessionName := seedResumableRun(t, svc, root, sess, []string{"one", "two"}, RunInterrupted, 0, true)

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("ResumeRun State = %v, want RunSucceeded", run.State)
	}

	_, info, err := db.LoadSession(context.Background(), sess.ContextPrincipal(), sessionName)
	if err != nil {
		t.Fatalf("LoadSession(%q): %v", sessionName, err)
	}
	if info.MessageCount < 2 {
		t.Fatalf("catalog message count = %d, want >= 2 (both resumed steps' turns), want each resumed checkpoint to re-save the snapshot", info.MessageCount)
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

// TestResumeRunLoadsThroughSpawnerWithoutSecondChatLoad pins the resume
// session contract: the run's saved session comes back through
// GetOrResumeInDir exactly once, and the caller never issues its own
// chat Load afterwards - the implementor already restored history. The
// instrumented store counts LoadSession calls, so any second Load shows
// up as a second catalog read beyond the spawner's own restore.
func TestResumeRunLoadsThroughSpawnerWithoutSecondChatLoad(t *testing.T) {
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
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one"}, RunInterrupted, 0, true)

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("ResumeRun State = %v, want RunSucceeded", run.State)
	}
	if got := spawner.getOrResumeCallCount(); got != 1 {
		t.Fatalf("GetOrResumeInDir called %d times, want exactly 1 (the resume must go through the spawner)", got)
	}
	if store.loadSessionCalls != 1 {
		t.Fatalf("store LoadSession called %d times, want exactly 1 (the spawner's own restore; the caller must not Load again)", store.loadSessionCalls)
	}
}

// TestResumeRunRepointsSessionNameBeforeRunningWrite pins the re-point
// ORDER: a run whose stored SessionName resolves onto a session whose
// own id differs must have its row re-pointed (updateRunSession with the
// new id) BEFORE the RunRunning state write. The trigger lets the first
// two automation_runs UPDATEs through (the claim-token update, then the
// re-point) and fails the third - the RunRunning transition - so the
// durable row must already carry the new id when the resume errors.
func TestResumeRunRepointsSessionNameBeforeRunningWrite(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	fresh := newContextEnabledSession(t, db)
	spawner := &sessionSpawner{conv: newRecordingConversation(), sess: sess, resumeSess: fresh}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, oldName := seedResumableRun(t, svc, root, sess, []string{"one", "two"}, RunInterrupted, 0, true)
	if fresh.SessionID == oldName {
		t.Fatal("fixture: the fresh session's id equals the stored name")
	}
	if err := forceAutomationRunsUpdateFailuresAfter(t, db, 2); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}

	if _, err := svc.ResumeRun(context.Background(), runID); err == nil {
		t.Fatal("ResumeRun: got nil error, want the third UPDATE (the RunRunning write) to fail")
	}
	stored, ok, err := svc.getRun(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if stored.SessionName != fresh.SessionID {
		t.Fatalf("row session name = %q, want %q (updateRunSession must land BEFORE the RunRunning write the trigger failed)", stored.SessionName, fresh.SessionID)
	}
	if stored.State != RunInterrupted {
		t.Fatalf("row state = %v, want RunInterrupted (the failed write was the RunRunning transition)", stored.State)
	}
}

// TestResumeRunRepointsSessionNameAndCheckpointsUnderIt pins the
// observable consequences of the re-point on a successful resume: the
// durable row names the restored session's own id, the per-step
// checkpoint saves land under that NEW id, and the old row keeps its
// spawn-time snapshot untouched.
func TestResumeRunRepointsSessionNameAndCheckpointsUnderIt(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	fresh := newContextEnabledSession(t, db)
	conv := newRecordingConversation()
	conv.onSend = func() {
		if _, err := fresh.SendUser(context.Background(), "resumed step turn", io.Discard); err != nil {
			t.Errorf("SendUser during resumed step: %v", err)
		}
	}
	spawner := &sessionSpawner{conv: conv, sess: sess, resumeSess: fresh}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, oldName := seedResumableRun(t, svc, root, sess, []string{"one", "two"}, RunInterrupted, 0, true)

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("ResumeRun State = %v, want RunSucceeded", run.State)
	}
	stored, ok, err := svc.getRun(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if stored.SessionName != fresh.SessionID {
		t.Fatalf("row session name = %q, want the restored session's own id %q", stored.SessionName, fresh.SessionID)
	}
	_, info, err := db.LoadSession(context.Background(), fresh.ContextPrincipal(), fresh.SessionID)
	if err != nil {
		t.Fatalf("LoadSession(%q): %v", fresh.SessionID, err)
	}
	if info.MessageCount < 1 {
		t.Fatalf("catalog message count under the new id = %d, want >= 1 (checkpoints must save under the new name)", info.MessageCount)
	}
	_, oldInfo, err := db.LoadSession(context.Background(), sess.ContextPrincipal(), oldName)
	if err != nil {
		t.Fatalf("LoadSession(%q): %v", oldName, err)
	}
	if oldInfo.MessageCount != 0 {
		t.Fatalf("old row message count = %d, want 0 (no checkpoint may land under the stale name)", oldInfo.MessageCount)
	}
}

// TestResumeRunLiveLeaseConflictNamesLeaseNotMissingSession pins the
// error mapping for a live-lease conflict: a spawner refusal of type
// contextstate.SessionLiveError becomes a failure message naming the
// lease conflict, never the "no resumable session" wrap.
func TestResumeRunLiveLeaseConflictNamesLeaseNotMissingSession(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	spawner := &sessionSpawner{
		conv:           newRecordingConversation(),
		sess:           sess,
		getOrResumeErr: &contextstate.SessionLiveError{LeaseAge: 2 * time.Second, RetryAfter: 5 * time.Second},
	}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, _ := seedResumableRun(t, svc, root, sess, []string{"one"}, RunInterrupted, 0, true)

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: got error %v, want nil (failure is recorded on the run)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("ResumeRun State = %v, want RunFailed", run.State)
	}
	if !strings.Contains(run.Message, "in use by another mivia process") || !strings.Contains(run.Message, "lease") {
		t.Fatalf("run.Message = %q, want it naming the live-lease conflict", run.Message)
	}
	if strings.Contains(run.Message, ErrRunSessionMissing.Error()) {
		t.Fatalf("run.Message = %q, want a live-lease failure, not the %q wrap", run.Message, ErrRunSessionMissing.Error())
	}
}

// TestResumeRunChainedResumeContinuesFromRepointedRow proves a chain of
// two resumes of the same run stays coherent across the re-point. The
// run row starts with a legacy reserved SessionName that no longer
// matches any live session id, and the spawner resolves it onto a
// session carrying a fresh id (the plain-snapshot fork). The first
// resume re-points the row onto that fresh id, then fails mid-steps.
// The SECOND resume must go back through the spawner asking for the
// re-pointed id - not the legacy name - complete from the failed step,
// and leave a succeeded row that still carries the re-pointed name.
func TestResumeRunChainedResumeContinuesFromRepointedRow(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	fresh := newContextEnabledSession(t, db)
	conv := newRecordingConversation()
	conv.failAt = 2 // first resume fails on its second step
	spawner := &sessionSpawner{conv: conv, resumeSess: fresh}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	legacyName := "legacy-snapshot-name"
	_, runID := seedResumableRunWithSessionName(t, svc, root, legacyName, []string{"one", "two"})

	// First resume: re-point onto the restored session's own id, then a
	// mid-steps failure on the forced second-step error.
	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("first ResumeRun: got error %v, want nil (step failure is recorded on the run)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("first ResumeRun State = %v, want RunFailed", run.State)
	}
	stored, ok, err := svc.getRun(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("getRun after first resume: ok=%v err=%v", ok, err)
	}
	if stored.SessionName != fresh.SessionID {
		t.Fatalf("row session name after first resume = %q, want the re-pointed id %q", stored.SessionName, fresh.SessionID)
	}
	if stored.State != RunFailed || stored.StepIndex != 1 {
		t.Fatalf("row after first resume = (state %v, stepIndex %d), want (RunFailed, 1) so the failed step re-runs whole", stored.State, stored.StepIndex)
	}

	// Second resume of the same run id: must load through the re-pointed
	// id and finish the run.
	conv.failAt = 0
	run, err = svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("second ResumeRun: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("second ResumeRun State = %v, want RunSucceeded", run.State)
	}
	ids := spawner.getOrResumeRequestedIDs()
	want := []string{legacyName, fresh.SessionID}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("GetOrResumeInDir requested ids = %v, want %v (the second resume must ask for the re-pointed id, not the legacy name)", ids, want)
	}
	if got := conv.sentTexts(); len(got) != 3 || got[0] != "one" || got[1] != "two" || got[2] != "two" {
		t.Fatalf("sent texts = %v, want [one two two] (the second resume re-runs only the failed step)", got)
	}
	final, ok, err := svc.getRun(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("getRun after second resume: ok=%v err=%v", ok, err)
	}
	if final.State != RunSucceeded {
		t.Fatalf("terminal row state = %v, want RunSucceeded", final.State)
	}
	if final.SessionName != fresh.SessionID {
		t.Fatalf("terminal row session name = %q, want the re-pointed id %q intact", final.SessionName, fresh.SessionID)
	}
}

// TestResumeRunRepointFailureFailsRun covers the re-point's own error
// branch: the trigger fails the SECOND automation_runs UPDATE (the
// re-point's updateRunSession, right after the claim-token update), so
// the resume fails with the "record run session" wrap and the row stays
// at its stored state and name - a resume that cannot re-point must not
// proceed to run steps under a name it could not record.
func TestResumeRunRepointFailureFailsRun(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	sess := newContextEnabledSession(t, db)
	fresh := newContextEnabledSession(t, db)
	spawner := &sessionSpawner{conv: newRecordingConversation(), sess: sess, resumeSess: fresh}
	svc, err := New(root, db, spawner, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, runID, oldName := seedResumableRun(t, svc, root, sess, []string{"one", "two"}, RunInterrupted, 0, true)
	if fresh.SessionID == oldName {
		t.Fatal("fixture: the fresh session's id equals the stored name")
	}
	if err := forceAutomationRunsUpdateFailuresAfter(t, db, 1); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}

	run, err := svc.ResumeRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	// The trigger fails every later UPDATE, so the failure itself cannot
	// be recorded either: the row stays exactly as it was seeded. The
	// observable contract is that the resume did NOT proceed to run
	// steps and did NOT re-point.
	if run.State != ports.RunInterrupted {
		t.Fatalf("run state %v, want interrupted", run.State)
	}
	if spawner.getOrResumeCalls != 1 {
		t.Fatalf("GetOrResumeInDir called %d times, want 1 (the failure came after the spawn)", spawner.getOrResumeCalls)
	}
	stored, ok, err := svc.getRun(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("getRun: ok=%v err=%v", ok, err)
	}
	if stored.SessionName != oldName {
		t.Fatalf("row session name = %q, want the unchanged %q (the re-point failed)", stored.SessionName, oldName)
	}
}
