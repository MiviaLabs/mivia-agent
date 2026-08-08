package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// restoreTestModel wires a file session store with one saved session "s1"
// onto a ready chat model rooted at root.
func restoreTestModel(t *testing.T, root string) *tuiModel {
	t.Helper()
	m := newReadyChatModel(30, 90)
	dir := workspace.SessionsDir(root)
	store, err := chat.NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.session.SetSessionStore(store, chat.NewSaveManager(store, "m", "p"))
	m.session.Messages = append(m.session.Messages, provider.Message{Role: provider.RoleUser, Content: "hi"})
	if err := m.session.Save("s1"); err != nil {
		t.Fatalf("save session: %v", err)
	}
	return m
}

// TestOpenSessionRestartsInWorktree verifies that a selected session rebuilds
// its runtime in the worktree that owns its saved directory.
func TestOpenSessionRestartsInWorktree(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "test")
	runGit(t, root, "commit", "--allow-empty", "-m", "init")
	wtPath := filepath.Join(root, ".mivia", "worktrees", "wt-a")
	runGit(t, root, "worktree", "add", wtPath, "-b", "wt/wt-a", "HEAD")

	m := restoreTestModel(t, root)
	m.workspaceDir = root
	m.sessions = []chat.SessionInfo{{Name: "s1", Dir: wtPath}}
	m.sessionSel = 0

	if err := m.openSessionByName("s1"); err != nil {
		t.Fatalf("openSessionByName: %v", err)
	}
	if m.restartWorkspace != wtPath || m.resumeSessionName != "s1" {
		t.Fatalf("restart = (%q, %q), want (%q, %q)", m.restartWorkspace, m.resumeSessionName, wtPath, "s1")
	}
	got, err := os.Getwd()
	if err != nil || filepath.Clean(got) != filepath.Clean(orig) {
		t.Fatalf("open changed cwd to %q, err=%v", got, err)
	}
}

func TestOpenWorktreeRouteRestartsBeforeLoadingSession(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	root := t.TempDir()
	route := t.TempDir()
	m := restoreTestModel(t, root)
	m.workspaceDir = root
	m.sessions = []chat.SessionInfo{{Name: "worktree:wt-a", Dir: route, Worktree: "wt-a", WorktreeRoute: true}}

	if err := m.openSessionByName("worktree:wt-a"); err != nil {
		t.Fatalf("open worktree route: %v", err)
	}
	if m.restartWorkspace != route {
		t.Fatalf("restart workspace = %q, want %q", m.restartWorkspace, route)
	}
	if cwd, err := os.Getwd(); err != nil || filepath.Clean(cwd) != filepath.Clean(orig) {
		t.Fatalf("route open changed cwd to %q, err=%v", cwd, err)
	}
}

func TestOpenSelectedSessionKeepsWorktreeRouteIdentityOnNameCollision(t *testing.T) {
	root := t.TempDir()
	route := t.TempDir()
	m := restoreTestModel(t, root)
	if err := m.session.Save("worktree:wt-a"); err != nil {
		t.Fatalf("save colliding snapshot: %v", err)
	}
	m.sessions = []chat.SessionInfo{
		{Name: "worktree:wt-a", Dir: route, Worktree: "wt-a", WorktreeRoute: true},
		{Name: "worktree:wt-a"},
	}

	m.sessionSel = 0
	if err := m.openSelectedSession(); err != nil {
		t.Fatalf("open selected route: %v", err)
	}
	if m.restartWorkspace != route {
		t.Fatalf("route restart workspace = %q, want %q", m.restartWorkspace, route)
	}

	m.restartWorkspace = ""
	m.sessionSel = 1
	if err := m.openSelectedSession(); err != nil {
		t.Fatalf("open selected snapshot: %v", err)
	}
	if m.restartWorkspace != "" {
		t.Fatalf("snapshot restart workspace = %q, want empty", m.restartWorkspace)
	}
	if m.mode != modeChat {
		t.Fatalf("mode = %v, want chat", m.mode)
	}
}

// TestOpenSessionRefusesRestartWhileAgentRunning verifies the waiting guard.
func TestOpenSessionRefusesRestartWhileAgentRunning(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "test")
	runGit(t, root, "commit", "--allow-empty", "-m", "init")

	other := t.TempDir()
	m := restoreTestModel(t, root)
	m.workspaceDir = root
	m.sessions = []chat.SessionInfo{{Name: "s1", Dir: other}}
	m.waiting = true

	if err := m.openSessionByName("s1"); err == nil || !strings.Contains(err.Error(), "cannot switch") {
		t.Fatalf("openSessionByName error = %v, want switch refusal", err)
	}
	got, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) == filepath.Clean(other) {
		t.Fatal("must not chdir while an agent turn is in flight")
	}
}

// TestOpenSessionRejectsMissingDirectory verifies a session cannot load when
// its recorded workspace no longer exists.
func TestOpenSessionRejectsMissingDirectory(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "test")
	runGit(t, root, "commit", "--allow-empty", "-m", "init")

	gone := filepath.Join(root, "does-not-exist")
	m := restoreTestModel(t, root)
	m.workspaceDir = root
	m.sessions = []chat.SessionInfo{{Name: "s1", Dir: gone}}

	if err := m.openSessionByName("s1"); err == nil || !strings.Contains(err.Error(), "workspace is unavailable") {
		t.Fatalf("openSessionByName error = %v, want unavailable workspace", err)
	}
	got, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) == filepath.Clean(gone) {
		t.Fatal("cwd must not move to a missing directory")
	}
}

// TestSessionPickerShowsWorktreeMarker verifies the welcome picker shows a
// worktree marker so the user can tell which session lives in which worktree.
func TestSessionPickerShowsWorktreeMarker(t *testing.T) {
	now := time.Now()
	sessions := []chat.SessionInfo{
		{Name: "a", MessageCount: 2, UpdatedAt: now, Worktree: "wt-x"},
		{Name: "b", MessageCount: 1, UpdatedAt: now},
	}
	lines, _, _ := renderSessionRows(sessions, 0, 0, 80, 5, 10)
	if !strings.Contains(strings.Join(lines, "\n"), "⊞ wt-x") {
		t.Fatalf("picker must show the worktree marker:\n%s", strings.Join(lines, "\n"))
	}
}
