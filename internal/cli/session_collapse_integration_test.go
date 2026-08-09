package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

const collapseOpener = "same opener for the collapse test"

// collapseFixture is the shared scenario for the collapse integration tests:
// three continuations of one conversation, one distinct conversation, one
// worktree route, and a ready TUI model whose list is refreshed.
type collapseFixture struct {
	store    *storage.SQLite
	res      *config.Resolved
	root     string
	m        *tuiModel
	sessions []*chat.Session
	newest   *chat.SessionInfo
}

func newCollapseFixture(t *testing.T) *collapseFixture {
	t.Helper()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	res := &config.Resolved{ProviderName: "test", Model: "test"}
	root := t.TempDir()
	fixture := &collapseFixture{store: store, res: res, root: root}

	for i := 0; i < 3; i++ {
		s := chat.NewSession(res, welcomeStubCompleter{})
		setupTitleTestContext(t, s, store, root)
		if _, err := s.SendUser(context.Background(), collapseOpener, io.Discard); err != nil {
			t.Fatal(err)
		}
		fixture.sessions = append(fixture.sessions, s)
	}
	distinct := chat.NewSession(res, welcomeStubCompleter{})
	setupTitleTestContext(t, distinct, store, root)
	if _, err := distinct.SendUser(context.Background(), "a genuinely different conversation", io.Discard); err != nil {
		t.Fatal(err)
	}

	routePrincipal, err := contextstate.NewPrincipal(contextWorkspaceID(root), "route-session", "local-user")
	if err != nil {
		t.Fatal(err)
	}
	routeDir := filepath.Join(root, "wt-a")
	if err := os.MkdirAll(routeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorktreeRoute(context.Background(), routePrincipal, "wt-a", routeDir); err != nil {
		t.Fatal(err)
	}

	fixture.m = newTUIModel(fixture.sessions[0], res, true)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixture.m.workspaceDir = cwd
	if err := fixture.m.refreshSessionList(); err != nil {
		t.Fatal(err)
	}
	fixture.newest = newestContinuation(t, fixture.sessions)
	return fixture
}

// newestContinuation returns the group member with the latest update from the
// raw catalog list.
func newestContinuation(t *testing.T, sessions []*chat.Session) *chat.SessionInfo {
	t.Helper()
	raw, err := sessions[0].ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	var newest *chat.SessionInfo
	for i := range raw {
		info := raw[i]
		inGroup := false
		for _, s := range sessions {
			if info.SessionID == s.SessionID {
				inGroup = true
				break
			}
		}
		if !inGroup {
			continue
		}
		if newest == nil {
			cp := info
			newest = &cp
			continue
		}
		if info.UpdatedAt.After(newest.UpdatedAt) {
			cp := info
			newest = &cp
		}
	}
	if newest == nil {
		t.Fatalf("no continuation rows found for %d sessions", len(sessions))
	}
	return newest
}

// TestIntegrationSessionListCollapsesForkedContinuations pins the fix for the
// sidebar showing one raw session ID row per mivia run: sessions that are
// continuations of the same conversation (same first user message) collapse to
// a single entry in the user-facing list. Worktree routes stay separate.
func TestIntegrationSessionListCollapsesForkedContinuations(t *testing.T) {
	fixture := newCollapseFixture(t)
	m := fixture.m

	if len(m.sessions) != 3 {
		t.Fatalf("collapsed list length = %d, want 3 (one per conversation + route): %#v", len(m.sessions), m.sessions)
	}
	foundGroup := false
	for _, info := range m.sessions {
		if info.SessionID == fixture.newest.SessionID {
			foundGroup = true
		}
		if info.SessionID == fixture.newest.SessionID || info.Worktree != "" || info.WorktreeRoute {
			continue
		}
		for _, s := range fixture.sessions {
			if info.SessionID == s.SessionID {
				t.Fatalf("collapsed list still shows continuation %q", info.SessionID)
			}
		}
	}
	if !foundGroup {
		t.Fatalf("collapsed list misses the newest continuation %q: %#v", fixture.newest.SessionID, m.sessions)
	}
}

// TestIntegrationCollapseOpenLoadsNewest pins that opening the collapsed row
// loads the newest continuation of the conversation.
func TestIntegrationCollapseOpenLoadsNewest(t *testing.T) {
	fixture := newCollapseFixture(t)
	m := fixture.m

	m.sessionSel = indexOfSession(t, m.sessions, fixture.newest.SessionID)
	if err := m.openSelectedSession(); err != nil {
		t.Fatal(err)
	}
	if m.activeSession == nil || m.activeSession.Reference() != fixture.newest.SessionID {
		t.Fatalf("opened session = %#v, want %q", m.activeSession, fixture.newest.SessionID)
	}
}

// TestIntegrationCollapseDeleteRemovesGroup pins that deleting the collapsed
// entry removes the whole conversation, not just the visible row.
func TestIntegrationCollapseDeleteRemovesGroup(t *testing.T) {
	fixture := newCollapseFixture(t)
	m := fixture.m

	sidebar := newSessionsSidebar()
	m.sessionsSidebar = sidebar
	sidebar.move(m.sessions, indexOfSession(t, m.sessions, fixture.newest.SessionID)+1)
	sidebar.confirm = confirmDeleteOne
	m.applySidebarSessionsConfirm()

	raw, err := m.session.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range fixture.sessions {
		for _, info := range raw {
			if info.SessionID == s.SessionID {
				t.Fatalf("deleting the collapsed entry left continuation %q in the catalog", s.SessionID)
			}
		}
	}
	if len(raw) != 2 {
		t.Fatalf("after group delete raw list = %d rows, want 2 (distinct + route): %#v", len(raw), raw)
	}
}

// TestIntegrationCollapsePurgeKeepsRoutes pins that purge-all removes every
// non-route row and keeps worktree routes.
func TestIntegrationCollapsePurgeKeepsRoutes(t *testing.T) {
	fixture := newCollapseFixture(t)
	m := fixture.m

	sidebar := newSessionsSidebar()
	m.sessionsSidebar = sidebar
	sidebar.confirm = confirmPurgeAll
	m.applySidebarSessionsConfirm()

	raw, err := m.session.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || !raw[0].WorktreeRoute {
		t.Fatalf("after purge raw list = %#v, want only the worktree route", raw)
	}
}

// TestIntegrationSidebarDeleteRemovesAutoSaveRow pins that deleting a visible
// auto-save row (legacy file-store mode) actually removes it. The sidebar
// shows one row per conversation; the group delete must always delete the
// visible row itself, never report success while leaving it on disk.
func TestIntegrationSidebarDeleteRemovesAutoSaveRow(t *testing.T) {
	dir := t.TempDir()
	store, err := chat.NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := newReadyChatModel(30, 90)
	m.session.SetSessionStore(store, chat.NewSaveManager(store, "m", "p"))
	m.session.Messages = append(m.session.Messages,
		provider.Message{Role: provider.RoleUser, Content: "legacy opener"},
		provider.Message{Role: provider.RoleAssistant, Content: "ok"})
	if err := m.session.SaveLast(); err != nil {
		t.Fatalf("save last: %v", err)
	}
	if err := m.refreshSessionList(); err != nil {
		t.Fatal(err)
	}
	if len(m.sessions) != 1 || !chat.IsAutoSaveName(m.sessions[0].Name) {
		t.Fatalf("legacy list = %#v, want one auto-save row", m.sessions)
	}

	sidebar := newSessionsSidebar()
	m.sessionsSidebar = sidebar
	sidebar.move(m.sessions, 1)
	sidebar.confirm = confirmDeleteOne
	m.applySidebarSessionsConfirm()

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("auto-save row survived the delete: %#v", infos)
	}
}

func indexOfSession(t *testing.T, infos []chat.SessionInfo, sessionID string) int {
	t.Helper()
	for i, info := range infos {
		if info.SessionID == sessionID {
			return i
		}
	}
	t.Fatalf("session %q not in %#v", sessionID, infos)
	return -1
}

// TestIntegrationSessionListDisplaysTitlesNotRawIDs pins that the user-facing
// list renders the derived first-message title instead of a raw session ID.
func TestIntegrationSessionListDisplaysTitlesNotRawIDs(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := &config.Resolved{ProviderName: "test", Model: "test"}
	root := t.TempDir()
	s := chat.NewSession(res, welcomeStubCompleter{})
	setupTitleTestContext(t, s, store, root)
	if _, err := s.SendUser(context.Background(), "readable first message for display", io.Discard); err != nil {
		t.Fatal(err)
	}

	m := newTUIModel(s, res, true)
	if err := m.refreshSessionList(); err != nil {
		t.Fatal(err)
	}
	if len(m.sessions) != 1 {
		t.Fatalf("list length = %d, want 1", len(m.sessions))
	}
	view := stripANSI(newSessionsSidebar().viewWithActive(m.sessions, 40, 10, true, nil, liveStatusIdle))
	if !strings.Contains(view, "readable first message for display") {
		t.Fatalf("sidebar does not show the derived title:\n%s", view)
	}
	if strings.Contains(view, s.SessionID) {
		t.Fatalf("sidebar shows the raw session ID %q:\n%s", s.SessionID, view)
	}
}
