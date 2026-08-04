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

// TestOpenSessionRestoresWorktreeDirectory is the regression test for the
// reported gap: opening a session that was saved inside a worktree must
// chdir back into that worktree and refresh the TUI git context.
func TestOpenSessionRestoresWorktreeDirectory(t *testing.T) {
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
	got, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) != filepath.Clean(wtPath) {
		t.Fatalf("cwd = %q, want the session directory %q", got, wtPath)
	}
	if m.gitWorktreeName != "wt-a" {
		t.Fatalf("gitWorktreeName = %q, want wt-a (TUI context must refresh)", m.gitWorktreeName)
	}
	if filepath.Clean(m.workspaceDir) != filepath.Clean(wtPath) {
		t.Fatalf("workspaceDir = %q, want %q", m.workspaceDir, wtPath)
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

// TestOpenSessionSkipsRestoreWhileAgentRunning verifies the waiting guard:
// a session whose directory differs is loaded but the process never chdirs
// while an agent turn is in flight.
func TestOpenSessionSkipsRestoreWhileAgentRunning(t *testing.T) {
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

	if err := m.openSessionByName("s1"); err != nil {
		t.Fatalf("openSessionByName: %v", err)
	}
	got, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) == filepath.Clean(other) {
		t.Fatal("must not chdir while an agent turn is in flight")
	}
	found := false
	for _, msg := range m.messages {
		if strings.Contains(msg, "cannot switch directory") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a notice about the refused directory switch, messages: %q", m.messages)
	}
}

// TestOpenSessionNoticesMissingDirectory verifies a session whose recorded
// directory no longer exists loads without chdir and explains why.
func TestOpenSessionNoticesMissingDirectory(t *testing.T) {
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

	if err := m.openSessionByName("s1"); err != nil {
		t.Fatalf("openSessionByName: %v", err)
	}
	got, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) == filepath.Clean(gone) {
		t.Fatal("cwd must not move to a missing directory")
	}
	found := false
	for _, msg := range m.messages {
		if strings.Contains(msg, "no longer exists") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a notice about the missing directory, messages: %q", m.messages)
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
	lines, _, _ := renderSessionRows(sessions, 0, 0, 5, 10)
	if !strings.Contains(strings.Join(lines, "\n"), "⊞ wt-x") {
		t.Fatalf("picker must show the worktree marker:\n%s", strings.Join(lines, "\n"))
	}
}

// TestSessionsDialogShowsWorktreeMarker verifies the /sessions dialog rows
// carry the same worktree marker.
func TestSessionsDialogShowsWorktreeMarker(t *testing.T) {
	d := newSessionsDialog([]chat.SessionInfo{{Name: "a", MessageCount: 2, UpdatedAt: time.Now(), Worktree: "wt-x"}})
	rows := d.rowLines(60, 10)
	if !strings.Contains(strings.Join(rows, "\n"), "⊞ wt-x") {
		t.Fatalf("dialog must show the worktree marker:\n%s", strings.Join(rows, "\n"))
	}
}
