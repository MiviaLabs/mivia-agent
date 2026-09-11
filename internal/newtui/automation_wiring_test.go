package newtui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestNewAutomationSpawnerReachesPoolCreateFreshInDir covers
// newAutomationSpawner's own wiring (the value wireAutomationBackend
// installs on the Service, distinct from the standalone
// automationSessionSpawner adapter TestAutomationSessionSpawnerCallsUnderlyingPool
// below already proves forwards its arguments correctly): a SessionPool
// built with a nil config (uiadapter.NewSessionPool(nil, nil, nil, ...))
// makes CreateFreshInDir fail fast and cheaply (SessionPool.res == nil
// guard, session_pool_worktree.go) without spawning a real session,
// which is enough to prove the closure genuinely reaches
// pool.CreateFreshInDir - not merely that it type-checks.
func TestNewAutomationSpawnerReachesPoolCreateFreshInDir(t *testing.T) {
	pool := uiadapter.NewSessionPool(nil, nil, nil, false)
	spawn := newAutomationSpawner(pool)
	bindCalled := false
	bindFn := func(*chat.Session) (string, error) {
		bindCalled = true
		return "", nil
	}
	if _, err := spawn.CreateFreshInDir(bindFn, "/some/dir"); err == nil {
		t.Fatal("CreateFreshInDir through the wired spawner with a nil-config pool: got nil error, want the pool's own 'no config provided' failure")
	}
	if bindCalled {
		t.Fatal("bind was called despite the pool's own nil-config guard firing first")
	}
}

// TestAutomationSessionSpawnerCallsUnderlyingPool covers
// automationSessionSpawner.CreateFreshInDir's own method body (run.go): the
// standalone adapter type, tested in isolation from newAutomationSpawner's
// own pool-specific wiring above, proving the plain-func-to-
// uiadapter.BindFunc conversion forwards both
// arguments and both return values unchanged. Uses a real, nil-config
// SessionPool (as TestNewAutomationSpawnerReachesPoolCreateFreshInDir does)
// since automationSessionSpawner now wraps a concrete *uiadapter.SessionPool
// rather than an injectable closure (SessionSpawner has 2 methods,
// which a bare func type cannot satisfy).
func TestAutomationSessionSpawnerCallsUnderlyingPool(t *testing.T) {
	pool := uiadapter.NewSessionPool(nil, nil, nil, false)
	var target automation.SessionSpawner = automationSessionSpawner{pool: pool} // pins that automationSessionSpawner satisfies automation.SessionSpawner
	bindCalled := false
	bindFn := func(*chat.Session) (string, error) {
		bindCalled = true
		return "", nil
	}
	conv, err := target.CreateFreshInDir(bindFn, "/some/dir")
	if err == nil {
		t.Fatal("CreateFreshInDir through automationSessionSpawner with a nil-config pool: got nil error, want the pool's own 'no config provided' failure")
	}
	if conv != nil {
		t.Fatalf("CreateFreshInDir returned a non-nil conversation on error: %v", conv)
	}
	if bindCalled {
		t.Fatal("bind was called despite the pool's own nil-config guard firing first")
	}
}

// TestAutomationSessionSpawnerSetApprovalOverride covers
// automationSessionSpawner.SetApprovalOverride's own method body: it
// forwards to the pool's own SetApprovalOverride, proven here by the
// pool's own "unknown session" rejection for an id it has never pooled.
func TestAutomationSessionSpawnerSetApprovalOverride(t *testing.T) {
	pool := uiadapter.NewSessionPool(nil, nil, nil, false)
	spawn := automationSessionSpawner{pool: pool}
	err := spawn.SetApprovalOverride("no-such-session", nil, "deny")
	if err == nil {
		t.Fatal("SetApprovalOverride through automationSessionSpawner for an unknown session: got nil error, want rejection")
	}
}

// TestWireAutomationBackendInstallsBackendOnSuccess covers
// wireAutomationBackend's success path directly (run.go), independent
// of buildApp's own end-to-end coverage of the same call: a real
// SettingsStore/SessionPool/Session/AgentSessionState quadruple is
// built the same way TestBuildApp's fixtures are, and the test asserts
// the backend actually landed on the store (SetAutomationBackend was
// reached), not merely that buildApp returned no error.
func TestWireAutomationBackendInstallsBackendOnSuccess(t *testing.T) {
	// [subagents] store_path pins the fallback store this test's
	// unwired sess.ContextStore() forces wireAutomationBackend to open
	// (see TestWireAutomationBackendFallbackOpensSharedContextStorePath
	// below) under this test's own temp root, instead of the default
	// shared, HOME-scoped store - a `go test` run must never touch the
	// real developer machine's home directory.
	res := &config.Resolved{Subagents: config.SubagentConfig{StorePath: ".mivia/context.db"}}
	sess := chat.NewSession(res, nil)
	agentState := &cli.AgentSessionState{WorkspaceRoot: t.TempDir()}
	store := uiadapter.NewSettingsStore(sess, res, agentState)
	runner := uiadapter.NewCommandRunner(sess, res, agentState)
	pool := runner.Pool()

	wireAutomationBackend(store, pool, sess, agentState, res)

	// Automations() delegating to a real backend (rather than the
	// in-memory fallback settings_automations.go carries when
	// automationBackend is nil) returns an empty, non-nil-erroring
	// slice for a workspace with no automations.toml yet - the
	// in-memory fallback would ALSO return empty for a fresh store, so
	// this alone would not distinguish backend-installed from
	// backend-absent. Apply a real edit instead: only a genuine
	// automation.Service backend persists it to automations.toml AND
	// makes it resolvable through LoadSpecs directly, independent of
	// the SettingsStore's own in-memory slice.
	h, err := store.Settings().Automations.Apply(context.Background(), ports.ScopeProject, ports.UpsertAutomation{
		Automation: ports.Automation{ID: "wired", Name: "wired", Action: ports.ActionRef{
			Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: "p"}},
		}},
	})
	if err != nil {
		t.Fatalf("Apply through the wired store: %v", err)
	}
	for range h.Events() {
	}
	specs, err := automation.LoadSpecs(ports.ScopeProject, agentState.WorkspaceRoot)
	if err != nil {
		t.Fatalf("LoadSpecs after Apply: %v", err)
	}
	if len(specs) != 1 || specs[0].ID != "wired" {
		t.Fatalf("automations.toml after Apply = %+v, want one automation persisted by the real backend Service", specs)
	}
}

// TestWireAutomationBackendFallbackOpensSharedContextStorePath covers
// wireAutomationBackend's own fallback branch (run.go): when
// sess.ContextStore() is NOT a *storage.SQLite, the Service must still
// get a real backing store, opened at the exact SAME path
// cli.ContextStorePath(root, res.Subagents) resolves - the path
// internal/cliautomations's openAutomationStore now also resolves for
// the CLI surface (store_path_test.go). A path mismatch here would
// silently reintroduce the split-history bug this slice fixes, just for
// the TUI's own fallback path instead of its now-removed normal one.
func TestWireAutomationBackendFallbackOpensSharedContextStorePath(t *testing.T) {
	root := t.TempDir()
	res := &config.Resolved{Subagents: config.SubagentConfig{StorePath: ".mivia/context.db"}}
	sess := chat.NewSession(res, nil) // ContextStore() is nil, not *storage.SQLite
	agentState := &cli.AgentSessionState{WorkspaceRoot: root}
	store := uiadapter.NewSettingsStore(sess, res, agentState)
	pool := uiadapter.NewCommandRunner(sess, res, agentState).Pool()

	closeFn := wireAutomationBackend(store, pool, sess, agentState, res)
	if closeFn == nil {
		t.Fatal("wireAutomationBackend returned a nil closer")
	}
	defer closeFn()

	wantPath := cli.ContextStorePath(root, res.Subagents)
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("wireAutomationBackend's fallback did not create a store at cli.ContextStorePath(%q, ...) = %q: %v", root, wantPath, err)
	}
}

// newSQLiteSession builds a session whose ContextStore() is a real
// *storage.SQLite, so wireAutomationBackend hands the Service a run
// store. The session has no context manager, so SetContextStore only
// records the store; nothing is armed.
func newSQLiteSession(t *testing.T, res *config.Resolved) (*chat.Session, *storage.SQLite) {
	t.Helper()
	db, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ctx.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sess := chat.NewSession(res, nil)
	if err := sess.SetContextStore(db); err != nil {
		t.Fatalf("SetContextStore: %v", err)
	}
	if _, ok := sess.ContextStore().(*storage.SQLite); !ok {
		t.Fatalf("ContextStore() = %T, want *storage.SQLite", sess.ContextStore())
	}
	return sess, db
}

// seedWiredAutomation writes one enabled single-step automation into
// root's automations.toml and returns its id.
func seedWiredAutomation(t *testing.T, root string) string {
	t.Helper()
	spec := automation.Spec{
		ID: "wired-close", Name: "wired-close", Enabled: true,
		Steps: []automation.Step{{Kind: automation.StepPrompt, Prompt: "p"}},
	}
	if err := automation.SaveSpecs(ports.ScopeProject, root, []automation.Spec{spec}); err != nil {
		t.Fatalf("SaveSpecs: %v", err)
	}
	return spec.ID
}

// drainSaveEvents reads h to close and returns the last event.
func drainSaveEvents(t *testing.T, h ports.SaveHandle) ports.SaveEvent {
	t.Helper()
	var last ports.SaveEvent
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-h.Events():
			if !ok {
				return last
			}
			last = ev
		case <-deadline:
			t.Fatal("SaveHandle did not close within 5s")
		}
	}
}

// TestWireAutomationBackendCloserClosesService proves the closer
// wireAutomationBackend returns reaches Service.Close: a trigger
// applied after the closer ran is refused as "service closed", and its
// row is not left running with a held claim.
func TestWireAutomationBackendCloserClosesService(t *testing.T) {
	res := &config.Resolved{}
	sess, db := newSQLiteSession(t, res)
	agentState := &cli.AgentSessionState{WorkspaceRoot: t.TempDir()}
	id := seedWiredAutomation(t, agentState.WorkspaceRoot)
	store := uiadapter.NewSettingsStore(sess, res, agentState)
	pool := uiadapter.NewCommandRunner(sess, res, agentState).Pool()

	closeFn := wireAutomationBackend(store, pool, sess, agentState, res)
	if closeFn == nil {
		t.Fatal("wireAutomationBackend returned a nil closer")
	}
	closeFn()

	h, err := store.Settings().Automations.Apply(context.Background(), ports.ScopeProject, ports.TriggerAutomation{ID: id})
	if err != nil {
		t.Fatalf("Apply after close: %v", err)
	}
	last := drainSaveEvents(t, h)
	if last.State != ports.SaveFailed || !strings.Contains(last.Message, automation.ErrServiceClosed.Error()) {
		t.Fatalf("final save event = %+v, want SaveFailed naming %q", last, automation.ErrServiceClosed.Error())
	}
	runs := store.Settings().Automations.Runs(id, 1)
	if len(runs) != 1 || runs[0].State != ports.RunFailed {
		t.Fatalf("runs after closed trigger = %+v, want one RunFailed row", runs)
	}
	if _, err := db.GetClaim(context.Background(), "automation:"+id); err == nil {
		t.Fatal("claim still held after the closer ran and the trigger was refused")
	}
}

// TestWireAutomationBackendSweepsInterruptedAtStart proves wiring runs
// the interrupted-run sweep: a running row with no claim, left by an
// earlier process, is RunInterrupted once wireAutomationBackend returns.
func TestWireAutomationBackendSweepsInterruptedAtStart(t *testing.T) {
	res := &config.Resolved{}
	sess, db := newSQLiteSession(t, res)
	agentState := &cli.AgentSessionState{WorkspaceRoot: t.TempDir()}
	id := seedWiredAutomation(t, agentState.WorkspaceRoot)
	ctx := context.Background()
	if err := db.InsertAutomationRun(ctx, storage.AutomationRun{
		ID: "run-orphan", AutomationID: id, Origin: "manual", State: "running",
		StepCount: 1, ClaimToken: "dead-holder", StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("InsertAutomationRun: %v", err)
	}
	store := uiadapter.NewSettingsStore(sess, res, agentState)
	pool := uiadapter.NewCommandRunner(sess, res, agentState).Pool()

	closeFn := wireAutomationBackend(store, pool, sess, agentState, res)
	defer closeFn()

	row, ok, err := db.GetAutomationRun(ctx, "run-orphan")
	if err != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, err)
	}
	if row.State != "interrupted" || row.EndedAt == nil {
		t.Fatalf("orphan row after wiring = state %q ended %v, want interrupted with ended_at set", row.State, row.EndedAt)
	}
}

// TestWireAutomationBackendCloserIsNoOpWhenWiringFails proves the
// closer is safe to call when automation.New refused the workspace: an
// empty root disables automations, and the returned closer does nothing.
func TestWireAutomationBackendCloserIsNoOpWhenWiringFails(t *testing.T) {
	res := &config.Resolved{}
	sess := chat.NewSession(res, nil)
	agentState := &cli.AgentSessionState{}
	store := uiadapter.NewSettingsStore(sess, res, agentState)
	pool := uiadapter.NewCommandRunner(sess, res, agentState).Pool()

	closeFn := wireAutomationBackend(store, pool, sess, agentState, res)
	if closeFn == nil {
		t.Fatal("wireAutomationBackend returned a nil closer on the failure path")
	}
	closeFn()
}

// stubTurnCompleter is the minimal provider.Completer the seeded session
// needs: SendUser runs a real turn through ChatTurn, which returns an
// empty successful response without any provider behind it.
type stubTurnCompleter struct{}

func (stubTurnCompleter) Name() string { return "stub" }
func (stubTurnCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return "", nil
}
func (stubTurnCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return "", nil
}
func (stubTurnCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	return &provider.Response{}, nil
}

// newContextBoundSession builds a session the way internal/automation's
// own test helper does: a bound Principal, an enabled ContextManager,
// and a *storage.SQLite store, so Save/Load round-trip through the store
// for real and the pool's entry inheritance can carry the store onto a
// resumed entry.
func newContextBoundSession(t *testing.T, res *config.Resolved, db *storage.SQLite, sessionID string) *chat.Session {
	t.Helper()
	sess := chat.NewSession(res, stubTurnCompleter{})
	sess.SessionID = sessionID
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatalf("NewPrincipal: %v", err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: db},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatalf("SetContextManager: %v", err)
	}
	if err := sess.SetContextStore(db); err != nil {
		t.Fatalf("SetContextStore: %v", err)
	}
	return sess
}

// TestAutomationSpawnerSpawnsBackgroundConversation pins the spawn
// posture through the real wiring value: a conversation the spawner's
// CreateFreshInDir produces must report IsBackground() true, so an
// automation run never shares the foreground TUI's progress registrar or
// tool-scope notice slot. Reverting run.go's CreateFreshBackgroundInDir
// call to a plain foreground spawn fails this test.
func TestAutomationSpawnerSpawnsBackgroundConversation(t *testing.T) {
	root := t.TempDir()
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	sess := chat.NewSession(res, nil)
	agentState := &cli.AgentSessionState{WorkspaceRoot: root}
	pool := uiadapter.NewCommandRunner(sess, res, agentState).Pool()

	conv, err := newAutomationSpawner(pool).CreateFreshInDir(nil, "")
	if err != nil {
		t.Fatalf("CreateFreshInDir through the wired spawner: %v", err)
	}
	c, ok := conv.(*uiadapter.Conversation)
	if !ok {
		t.Fatalf("spawner returned %T, want *uiadapter.Conversation", conv)
	}
	if !c.IsBackground() {
		t.Fatal("spawner's CreateFreshInDir produced a foreground conversation; run.go must spawn automation sessions via CreateFreshBackgroundInDir")
	}
}

// TestAutomationSpawnerGetOrResumeInDirRestoresSessionIdentity covers the
// adapter's GetOrResumeInDir on a pool's miss path: the returned session
// carries the requested id and the saved history is already restored, so
// the caller never calls Load again.
func TestAutomationSpawnerGetOrResumeInDirRestoresSessionIdentity(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	db, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ctx.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	seed := newContextBoundSession(t, res, db, "pool-seed-main")
	// A session-id-shaped row (26 base32 characters) with one real turn
	// of history, saved under the session's own id.
	resumeID := "MWIVAMWIVAMWIVAMWIVAMWIVAA"
	saved := newContextBoundSession(t, res, db, resumeID)
	if _, err := saved.SendUser(context.Background(), "seeded turn", io.Discard); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	if err := saved.Save(resumeID); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	pool := uiadapter.NewCommandRunner(seed, res, nil).Pool()
	t.Cleanup(pool.CloseAll)

	conv, gotSess, err := newAutomationSpawner(pool).GetOrResumeInDir(resumeID, "")
	if err != nil {
		t.Fatalf("GetOrResumeInDir: %v", err)
	}
	if gotSess == nil {
		t.Fatal("GetOrResumeInDir returned a nil session")
	}
	if gotSess.SessionID != resumeID {
		t.Fatalf("restored session id = %q, want %q", gotSess.SessionID, resumeID)
	}
	if len(conv.History()) == 0 {
		t.Fatal("restored conversation has empty history; the pool must restore it on the miss path")
	}
}
