package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	tea "github.com/charmbracelet/bubbletea"
)

func TestSetupSessionContextIsAlwaysEnabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	store, err := setupSessionContext(session, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !session.ContextEnabled() {
		t.Fatal("session context is disabled")
	}
	if _, ok := session.ContextStore().(*storage.SQLite); !ok {
		t.Fatalf("context store = %T, want SQLite", session.ContextStore())
	}
	if _, _, ok := session.ContextPreparation(); !ok {
		t.Fatal("session did not expose isolated preparation capability")
	}
}

func TestSetupSessionContextListsExistingSQLiteContextSessions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err := setupSessionContext(first, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.SendUser(context.Background(), "persist this", io.Discard); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	second := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err = setupSessionContext(second, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.SendUser(context.Background(), "second session", io.Discard); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	loader := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err = setupSessionContext(loader, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	infos, err := loader.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) == 0 {
		t.Fatal("SQLite context session was not presented in session list")
	}
	if len(infos) != 2 {
		t.Fatalf("SQLite context session list = %#v, want two sessions", infos)
	}
	for i, sessionID := range []string{first.SessionID, second.SessionID, first.SessionID, second.SessionID} {
		if err := loader.Load(sessionID); err != nil {
			t.Fatalf("load %d (%s): %v", i, sessionID, err)
		}
	}
	if got := loader.MessagesCopy(); len(got) < 1 || got[0].Content != "second session" {
		t.Fatalf("loaded SQLite context history = %#v", got)
	}
	if err := loader.DeleteSession(first.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := loader.DeleteSession(second.SessionID); err != nil {
		t.Fatal(err)
	}
	if infos, err := loader.ListSessions(); err != nil || len(infos) != 0 {
		t.Fatalf("sessions after delete = %#v, err=%v", infos, err)
	}
}

func TestListSessionsIncludesMainRepositoryWorktreeRoutes(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repoRoot, "route-target", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := registerWorktreeRoute(repoRoot, worktree); err != nil {
		t.Fatal(err)
	}
	containsRoute := func(infos []chat.SessionInfo) bool {
		for _, info := range infos {
			if info.WorktreeRoute && info.Worktree == worktree.Name && info.Dir == worktree.Path {
				return true
			}
		}
		return false
	}
	rootModel := newTUIModel(chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{}), nil, true)
	rootStore, err := setupSessionContext(rootModel.session, repoRoot, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer rootStore.Close()
	rootModel.workspaceDir = repoRoot
	rootInfos, err := rootModel.listSessions()
	if err != nil {
		t.Fatal(err)
	}
	if !containsRoute(rootInfos) {
		t.Fatalf("worktree route is missing from main repository sessions: %#v", rootInfos)
	}

	linkedModel := newTUIModel(chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{}), nil, true)
	store, err := setupSessionContext(linkedModel.session, worktree.Path, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	linkedModel.workspaceDir = worktree.Path

	infos, err := linkedModel.listSessions()
	if err != nil {
		t.Fatal(err)
	}
	if !containsRoute(infos) {
		t.Fatalf("worktree route is missing from linked-worktree sessions: %#v", infos)
	}
}

func TestSQLiteSessionsAreSelectableThroughSplashAndDialog(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(prompt string) string {
		s := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
		store, err := setupSessionContext(s, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SendUser(context.Background(), prompt, io.Discard); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		id := s.SessionID
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		return id
	}
	firstID, secondID := write("first history"), write("second history")
	loader := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err := setupSessionContext(loader, root, &config.Resolved{Subagents: config.DefaultSubagentConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	infos, err := loader.ListSessions()
	if err != nil || len(infos) != 2 {
		t.Fatalf("SQLite sessions = %#v, err=%v", infos, err)
	}
	indexOf := func(id string) int {
		for i, info := range infos {
			if info.Name == id {
				return i
			}
		}
		return -1
	}
	res := &config.Resolved{ProviderName: "fake", Model: "model"}
	m := newTUIModel(loader, res, true)
	m.mode, m.ready, m.width, m.height = modeWelcome, true, 100, 40
	m.sessions, m.sessionSel = infos, indexOf(firstID)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := loader.MessagesCopy(); len(got) == 0 || got[0].Content != "first history" {
		t.Fatalf("splash did not load first SQLite session: %#v", got)
	}
	for i, id := range []string{secondID, firstID, secondID} {
		m.mode = modeChat
		if !m.handleSlash("/sessions") {
			t.Fatal("/sessions was not handled")
		}
		m.sessionsDlg.cursor = indexOf(id)
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		want := "second history"
		if id == firstID {
			want = "first history"
		}
		got := loader.MessagesCopy()
		if len(got) == 0 || got[0].Content != want {
			t.Fatalf("dialog load %d (%s) = %#v, want %q", i, id, got, want)
		}
	}
}
