package conversation

// Program-level offline smoke test (repo memory: ui-ship-requires-
// offline-smoke-test): the REAL *uiadapter.CommandRunner wired into a
// REAL conversation Screen - the exact newtui.buildApp construction chain
// minus tea.Program/TTY - driving Enter over a /resume worktree route row
// and asserting the conversation actually swaps to a newly pooled session.
// Requires git on PATH; SQLite is pure-Go modernc; no network anywhere.

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

// smokeNullCompleter stands in for the provider; this package's offline
// tests never assert model output.
type smokeNullCompleter struct{}

func (smokeNullCompleter) Name() string { return "smoke-null" }
func (smokeNullCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	return "", nil
}
func (smokeNullCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (smokeNullCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{FinishReason: "stop"}, nil
}

// smokeWorktreeFixture registers managed worktree wt1 (instance + route)
// in a fresh repository store over a real temp git repo.
type smokeWorktreeCatalog struct {
	Store       *storage.SQLite
	MainDir     string
	WorktreeDir string
	DBPath      string
}

func smokeWorktreeFixture(t *testing.T) smokeWorktreeCatalog {
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
	runGit := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", mainDir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "test@example.invalid")
	runGit("config", "user.name", "test")

	dbPath := filepath.Join(t.TempDir(), "ctx.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	principal, err := worktreeroute.Principal(mainDir)
	if err != nil {
		t.Fatalf("derive principal: %v", err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt1", ID: "wt_0001020304050607"}
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, canonicalWt); err != nil {
		t.Fatalf("begin creation: %v", err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, canonicalWt); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	// The on-disk marker a managed worktree carries in production; the
	// TUI bind path verifies it against the live instance (REPL parity).
	markerDir := filepath.Join(canonicalWt, ".mivia")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatalf("marker dir: %v", err)
	}
	marker := []byte(`{"version":1,"worktree":"wt1","id":"wt_0001020304050607"}`)
	if err := os.WriteFile(filepath.Join(markerDir, "worktree-instance.json"), marker, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	return smokeWorktreeCatalog{Store: store, MainDir: mainDir, WorktreeDir: canonicalWt, DBPath: dbPath}
}

// smokeEnableCtx enables the durable-context path for one session,
// mirroring catalogSession in uiadapter_test.
func smokeEnableCtx(t *testing.T, sess *chat.Session, store *storage.SQLite, mainDir string) {
	t.Helper()
	principal, err := contextstate.NewPrincipal(worktreeroute.WorkspaceID(mainDir), sess.SessionID, "local-user")
	if err != nil {
		t.Fatalf("mint principal: %v", err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatalf("enable context: %v", err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatalf("install store: %v", err)
	}
}

// buildSmokeScreen mirrors newtui.buildApp: pool-sourced conversation,
// real CommandRunner as the screen's command seam, mandatory setters only.
func buildSmokeScreen(t *testing.T, sess *chat.Session, res *config.Resolved) (Screen, *uiadapter.CommandRunner) {
	t.Helper()
	approver := uiadapter.NewApprover(sess)
	th := loadTheme(t)
	themes, terr := theme.Embedded()
	if terr != nil {
		t.Fatalf("embedded themes: %v", terr)
	}
	runner := uiadapter.NewCommandRunner(sess, res, nil)
	convPort, err := runner.Pool().GetOrCreate(sess.SessionID)
	if err != nil {
		t.Fatalf("pool GetOrCreate: %v", err)
	}
	conv, ok := convPort.(*uiadapter.Conversation)
	if !ok {
		t.Fatalf("pool returned %T, want *uiadapter.Conversation", convPort)
	}
	s := New(th, theme.TierTrueColor, themes, conv, approver, 80, nil)
	s.SetCommands(runner.Commands())
	s.SetCommandRunner(runner)
	return s, runner
}

func TestSmoke_EnterOnResumeRouteRowSwitchesToARealPooledWorktreeSession(t *testing.T) {
	fx := smokeWorktreeFixture(t)
	store, mainDir := fx.Store, fx.MainDir
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess := chat.NewSession(res, smokeNullCompleter{})
	sess.SessionID = "session-main"
	smokeEnableCtx(t, sess, store, mainDir)

	s, runner := buildSmokeScreen(t, sess, res)
	t.Chdir(mainDir) // Root("") resolution must land on the fixture repo
	initialConv := s.conv
	s.width, s.height = 80, 24
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)

	// Deterministic two-Enter sequence: Enter #1 accepts the single
	// "/resume" completion candidate, Enter #2 submits the command.
	s, _ = sendLine(t, s, "/resume")
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	if s.sessionPicker == nil {
		t.Fatal("/resume did not open the session picker through the real runner listing")
	}

	idx := -1
	for i, row := range s.sessionPicker.visible() {
		if row.WorktreeRoute && row.Worktree == "wt1" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("route row wt1 not listed; visible rows:\n%+v", s.sessionPicker.visible())
	}
	for i := 0; i < idx; i++ {
		next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	if s.sessionPicker.cursor != idx {
		t.Fatalf("cursor=%d, want %d", s.sessionPicker.cursor, idx)
	}

	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	if s.sessionPicker != nil {
		t.Error("enter left the session picker open")
	}
	newID := s.conv.ID()
	if newID == "" || newID == sess.SessionID {
		t.Errorf("conversation did not switch: id=%q", newID)
	}
	if s.conv == initialConv {
		t.Error("screen still holds the initial conversation object")
	}
	if got := runner.Pool().Session(newID); got == nil {
		t.Errorf("new session %q not pooled on the runner's own pool", newID)
	}
	if got := runner.Pool().Session(sess.SessionID); got == nil || got != sess {
		t.Error("main session lost from the pool after a worktree start")
	}
	if detail := lastErrorDetail(t, s); !strings.Contains(detail, "wt1") {
		t.Errorf("notice detail %q does not reference wt1", detail)
	}
}
