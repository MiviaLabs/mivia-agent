package uiadapter_test

// Round-trip integration for TUI worktree-session resume: REAL persisted
// multi-turn catalog data flows save -> simulated process restart ->
// CommandRunner.ResumeInWorktree -> restored history -> further persisted
// turns under the kept worktree binding. Fully offline: scripted
// completers stand in for the provider; the temp git repo exists only so
// worktreeroute.Root resolves during resume; SQLite is pure-Go modernc.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

const (
	resumeFixtureInstanceID = "wt_0001020304050607"
	resumeSavedFirstName    = "wt-save"
	resumeSavedSecondName   = "wt-save-2"
)

// worktreeCatalogFixtureNoClose mirrors worktreeCatalogFixture but hands
// the handle and database path to the caller: restart simulation closes
// handle 1 and reopens the same path as an independent store.
// worktreeCatalog names the fixture's parts so call sites bind them by
// field, not by position - nine tests once bound the worktree dir as the
// repo root and stayed green (.agents/memories/positional-fixture-returns-
// invite-silent-misbinding.md).
type worktreeCatalog struct {
	Store       *storage.SQLite
	MainDir     string
	WorktreeDir string
	DBPath      string
}

func worktreeCatalogFixtureNoClose(t *testing.T) worktreeCatalog {
	t.Helper()
	mainDir := filepath.Join(t.TempDir(), "main")
	wtDir := filepath.Join(filepath.Dir(mainDir), ".mivia", "worktrees", "wt1")
	for _, dir := range []string{mainDir, wtDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create fixture dirs: %v", err)
		}
	}
	canonicalWt, err := worktreeroute.CanonicalDir(wtDir)
	if err != nil {
		t.Fatalf("canonicalize worktree dir: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "ctx.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	principal, err := worktreeroute.Principal(mainDir)
	if err != nil {
		t.Fatalf("derive principal: %v", err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt1", ID: resumeFixtureInstanceID}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, canonicalWt); err != nil {
		t.Fatalf("begin creation: %v", err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, canonicalWt); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	writeTestWorktreeMarker(t, canonicalWt, instance)
	return worktreeCatalog{Store: store, MainDir: mainDir, WorktreeDir: canonicalWt, DBPath: dbPath}
}

// installCtx enables the durable-context path exactly the way the CLI's
// enableSessionContext does: manager with a real committer over the
// store, then the store itself. Each session mints its own principal -
// workspace identity comes from the canonical repo root, subject stays
// "local-user" so listings and instance-scoped loads agree.
func installCtx(t *testing.T, sess *chat.Session, store *storage.SQLite, mainDir string) {
	t.Helper()
	principal, err := contextstate.NewPrincipal(worktreeroute.WorkspaceID(mainDir), sess.SessionID, "local-user")
	if err != nil {
		t.Fatalf("mint principal for %s: %v", sess.SessionID, err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatalf("enable context on %s: %v", sess.SessionID, err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatalf("install store on %s: %v", sess.SessionID, err)
	}
}

// withTools gives a test session a minimal tool registry so Send can run
// its admission machinery headless.
func withTools(sess *chat.Session) {
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(noopTool{})
}

// turn drives one user turn through the conversation port and drains its
// events to close, then waits on the turn-goroutine seam so checkpoint /
// persistence work has fully landed before the caller saves.
func turn(t *testing.T, conv ports.Conversation, text string) {
	t.Helper()
	var wg sync.WaitGroup
	uiadapter.SetTurnWaiterForTest(&wg)
	defer uiadapter.SetTurnWaiterForTest(nil)

	wg.Add(1)
	h, err := conv.Send(context.Background(), intent.Send{Text: text})
	if err != nil {
		t.Fatalf("Send %q: %v", text, err)
	}
	drainUntilClose(t, h.Events(), 5*time.Second)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn goroutine did not finish within 5s")
	}
}

// restartScenario bundles the simulated-restart state the focused tests
// assert against.
type restartScenario struct {
	store       *storage.SQLite // reopened handle after the simulated restart
	runner      *uiadapter.CommandRunner
	mainDir     string
	canonicalWt string
	resumedConv ports.Conversation
	resumedID   string
	principal   contextstate.Principal
}

// buildRestartScenario seeds two persisted sessions - one main-workspace,
// one bound to wt1 with two scripted turns - closes the store, reopens it
// as an independent handle, wires a fresh runner/pool over an unbound
// restart session, and resumes the saved worktree session through the
// REAL ResumeInWorktree path (pre-bind before Load, pool-carried).
func buildRestartScenario(t *testing.T) restartScenario {
	fx := worktreeCatalogFixtureNoClose(t)
	store1, mainDir, canonicalWt, dbPath := fx.Store, fx.MainDir, fx.WorktreeDir, fx.DBPath
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}

	compMain := &scriptedCompleter{turns: []provider.Response{assistantResponse("main answer")}}
	main := chat.NewSession(res, compMain)
	main.SessionID = "session-main"
	withTools(main)
	installCtx(t, main, store1, mainDir)
	runnerMain := uiadapter.NewCommandRunner(main, res, nil)
	convMainPort, err := runnerMain.Pool().GetOrCreate(main.SessionID)
	if err != nil {
		t.Fatalf("pool GetOrCreate main: %v", err)
	}
	turn(t, convMainPort, "main hello")
	if err := main.Save("main-save"); err != nil {
		t.Fatalf("save main: %v", err)
	}

	// Worktree-bound seed: bind FIRST (pre-context - exactly what the
	// pool's pre-bind hook guarantees), then enable context, mirroring the
	// REPL repository-binding order StartInRoute encodes.
	seedBoundWorktreeSession(t, store1, mainDir, canonicalWt, res)

	// Simulated process restart: independent SQLite handle, fresh session,
	// fresh pool, real runner over them.
	store1.Close()
	store2, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { store2.Close() })

	restart := chat.NewSession(res, &nullCompleter{})
	restart.SessionID = "session-main-restart"
	withTools(restart)
	installCtx(t, restart, store2, mainDir)
	pool2 := uiadapter.NewSessionPool(restart, res, nil, false)
	runner := uiadapter.NewCommandRunnerWithPool(restart, pool2, res, nil)
	t.Chdir(mainDir)

	out := runner.ResumeInWorktree(context.Background(), ports.SessionSummary{
		ID:          resumeSavedFirstName,
		Worktree:    "wt1",
		WorktreeDir: canonicalWt,
	})
	if out.Err != "" {
		t.Fatalf("ResumeInWorktree errored: %s", out.Err)
	}
	principal, err := worktreeroute.Principal(mainDir)
	if err != nil {
		t.Fatalf("rederive principal: %v", err)
	}
	return restartScenario{
		store:       store2,
		runner:      runner,
		mainDir:     mainDir,
		canonicalWt: canonicalWt,
		resumedConv: out.Conversation,
		resumedID:   out.Conversation.ID(),
		principal:   principal,
	}
}

// seedBoundWorktreeSession binds a fresh session to the fixture worktree
// (pre-context - exactly what the pool's pre-bind hook guarantees), then
// enables context, mirroring the REPL repository-binding order StartInRoute
// encodes, drives two scripted turns, and saves them under wt-save.
func seedBoundWorktreeSession(t *testing.T, store *storage.SQLite, mainDir, canonicalWt string, res *config.Resolved) {
	t.Helper()
	compWT := &scriptedCompleter{turns: []provider.Response{
		assistantResponse("wt answer one"),
		assistantResponse("wt answer two"),
	}}
	bound := chat.NewSession(res, compWT)
	bound.SessionID = "session-wt-seed"
	withTools(bound)
	if _, err := worktreeroute.StartInRoute(context.Background(), bound, store, mainDir,
		worktreeroute.Route{Worktree: "wt1", Dir: canonicalWt}); err != nil {
		t.Fatalf("bind seed session: %v", err)
	}
	installCtx(t, bound, store, mainDir)
	convBound := uiadapter.NewConversation(bound)
	turn(t, convBound, "wt turn one")
	turn(t, convBound, "wt turn two")
	if err := bound.Save(resumeSavedFirstName); err != nil {
		t.Fatalf("save bound session: %v", err)
	}
}

func TestResumeInWorktree_RestoresSavedMultiTurnThroughRestart(t *testing.T) {
	sc := buildRestartScenario(t)

	var got []string
	for _, m := range sc.resumedConv.History() {
		got = append(got, m.Text)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"wt turn one", "wt answer one", "wt turn two", "wt answer two"} {
		if !strings.Contains(joined, want) {
			t.Errorf("restored history missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "main hello") {
		t.Error("worktree-bound restore leaked main-workspace content")
	}
	if sc.resumedConv.ID() == "session-main-restart" || sc.resumedConv.ID() == "" {
		t.Fatalf("resumed conversation kept %q", sc.resumedConv.ID())
	}
}

func TestResumeInWorktree_KeepsBindingAcrossLaterTurnsAndSaves(t *testing.T) {
	sc := buildRestartScenario(t)

	resumedSess := sc.runner.Pool().Session(sc.resumedID)
	if resumedSess == nil {
		t.Fatal("pooled session vanished behind the resumed conversation")
	}

	// Boundary: a resumed process keeps its own live binding by design and
	// an offline fixture cannot configure a real provider runtime, so the
	// test re-binds to a scripted completer through the public API -
	// mirroring publishModelSwitch ordering. Restore (pool, bind, Load,
	// message adoption) and the plain-context commit path stay fully real.
	scriptedPost := &scriptedCompleter{turns: []provider.Response{assistantResponse("post-resume reply")}}
	if err := resumedSess.SwitchBinding(chat.ModelBinding{
		ProviderName: "fake",
		Model:        "m1",
		Completer:    scriptedPost,
	}); err != nil {
		t.Fatalf("rebind resumed session offline: %v", err)
	}

	turn(t, sc.resumedConv, "post-resume turn")

	if err := resumedSess.Save(resumeSavedSecondName); err != nil {
		t.Fatalf("post-resume save: %v", err)
	}

	var hist []string
	for _, m := range sc.resumedConv.History() {
		hist = append(hist, m.Text)
	}
	joined := strings.Join(hist, "\n")
	for _, want := range []string{"post-resume turn", "post-resume reply"} {
		if !strings.Contains(joined, want) {
			t.Errorf("post-resume turn not visible in history (missing %q):\n%s", want, joined)
		}
	}

	instance := contextstate.WorktreeInstance{Worktree: "wt1", ID: resumeFixtureInstanceID}
	infos, err := sc.store.ListWorktreeSessions(context.Background(), sc.principal, instance)
	if err != nil {
		t.Fatalf("ListWorktreeSessions: %v", err)
	}
	rows := map[string]contextstate.SessionCatalogInfo{}
	for _, info := range infos {
		rows[info.Name] = info
	}
	for _, name := range []string{resumeSavedFirstName, resumeSavedSecondName} {
		info, ok := rows[name]
		if !ok {
			t.Errorf("saved name %q absent from instance-scoped catalog (%d rows)", name, len(infos))
			continue
		}
		if info.Worktree != "wt1" || info.Dir != sc.canonicalWt {
			t.Errorf("row %q lost worktree metadata: %+v", name, info)
		}
	}
	if first, second := rows[resumeSavedFirstName].MessageCount, rows[resumeSavedSecondName].MessageCount; second <= first {
		t.Errorf("MessageCount did not grow after the extra turn: %d -> %d", first, second)
	}
}

func TestResumeInWorktree_InstanceRowsFailClosedForUnboundReader(t *testing.T) {
	sc := buildRestartScenario(t)

	reader := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "m1"}, &nullCompleter{})
	reader.SessionID = "reader-main"
	installCtx(t, reader, sc.store, sc.mainDir)

	err := reader.LoadReadOnly(resumeSavedFirstName)
	if err == nil {
		t.Fatal("unbound reader loaded an instance-scoped session")
	}
	if !strings.Contains(err.Error(), resumeSavedFirstName) {
		t.Errorf("error %q does not name the requested snapshot", err)
	}
	if err := reader.LoadReadOnly("main-save"); err != nil {
		t.Fatalf("unbound reader cannot see its own NULL-instance row: %v", err)
	}
}
