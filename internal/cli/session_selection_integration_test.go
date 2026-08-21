package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	tea "github.com/charmbracelet/bubbletea"
)

func persistedSessionForSelection(t *testing.T) (*chat.Session, *config.Resolved, []chat.SessionInfo, func()) {
	t.Helper()
	root := t.TempDir()
	res := &config.Resolved{
		ProviderName: "deepseek",
		Model:        "deepseek-v4-flash",
		Models:       []string{"deepseek-v4-flash"},
		APIKey:       "test-key",
		APIKeySet:    true,
		APIKeyEnv:    "DEEPSEEK_API_KEY",
		BaseURL:      "http://127.0.0.1:1",
		Subagents:    config.DefaultSubagentConfig,
	}
	comp, err := provider.New(res)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	sess := chat.NewSession(res, comp)
	sess.UseTools = true
	memClose, err := configureChatWorkspace(sess, root, true, res, nil, false, false, false)
	if err != nil {
		t.Fatalf("configure workspace: %v", err)
	}
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		return buildModelBinding(sess, res, root, providerName, model, &AgentSessionState{AllowProjectSkills: true})
	})
	cleanup, err := attachSessionDispatcher(sess, root, res.Model, res.Subagents, &AgentSessionState{AllowProjectSkills: true}, nil, sessionRouting{})
	if err != nil {
		t.Fatalf("attach dispatcher: %v", err)
	}
	// The memory store is session-owned: close it after the dispatcher
	// cleanup so Windows can remove the session's temp database.
	finish := func() { cleanup(); memClose() }
	sess.SessionDir = workspace.SessionsDir(root)
	store, err := chat.NewFileSessionStore(sess.SessionDir)
	if err != nil {
		finish()
		t.Fatalf("session store: %v", err)
	}
	sess.SetSessionStore(store, chat.NewSaveManager(store, res.Model, comp.Name()))
	sess.Messages = append(sess.Messages,
		provider.Message{Role: provider.RoleUser, Content: "previous question"},
		provider.Message{Role: provider.RoleAssistant, Content: "previous answer"},
	)
	if err := sess.Save("previous"); err != nil {
		finish()
		t.Fatalf("save session: %v", err)
	}
	infos, err := sess.ListSessions()
	if err != nil {
		finish()
		t.Fatalf("list sessions: %v", err)
	}
	_ = sess.Clear()
	return sess, res, infos, finish
}

func TestIntegrationSplashEnterLoadsPersistedSession(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()

	m := newTUIModel(sess, res, true)
	m.mode = modeWelcome
	m.ready = true
	m.width, m.height = 100, 40
	m.sessions = infos
	m.sessionSel = 0

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.mode != modeChat {
		t.Fatalf("splash Enter left mode=%v, want chat; view=%q", m.mode, stripANSI(m.View()))
	}
	if got := sess.MessagesCopy(); len(got) != 2 || got[0].Content != "previous question" {
		t.Fatalf("splash Enter did not restore persisted history: %#v", got)
	}
}

func TestIntegrationSessionsSidebarEnterLoadsPersistedSession(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()

	m := newTUIModel(sess, res, true)
	m.mode = modeChat
	m.ready = true
	m.width, m.height = 100, 40
	m.sessions = infos
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.sessionsSidebar == nil {
		t.Fatalf("Enter unexpectedly closed the sessions sidebar; view=%q", stripANSI(m.View()))
	}
	if got := sess.MessagesCopy(); len(got) != 2 || got[0].Content != "previous question" {
		t.Fatalf("dialog Enter did not restore persisted history: %#v; view=%q", got, stripANSI(m.View()))
	}
	if got := strings.Count(stripANSI(m.View()), "current"); got != 1 {
		t.Fatalf("loaded session current marker count = %d, want 1; view=%q", got, stripANSI(m.View()))
	}

	// Reopening the same saved session must rebuild a second dispatcher
	// generation without re-registering generation-owned tools.
	if !m.handleSlash("/sessions") {
		t.Fatal("second /sessions toggle was not handled")
	}
	if !m.handleSlash("/sessions") {
		t.Fatal("third /sessions reopen was not handled")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.sessionsSidebar == nil {
		t.Fatalf("second Enter unexpectedly closed the sessions sidebar; view=%q", stripANSI(m.View()))
	}
}

func TestTUISlashLoadMarksCurrentSidebarSession(t *testing.T) {
	sess, res, _, cleanup := persistedSessionForSelection(t)
	defer cleanup()

	m := newTUIModel(sess, res, true)
	m.mode, m.ready, m.width, m.height = modeChat, true, 100, 40
	m.sessions = nil
	if !m.handleSlash("/load previous") || !m.handleSlash("/sessions") {
		t.Fatal("sessions and load commands must be handled")
	}
	if got := strings.Count(stripANSI(m.View()), "current"); got != 1 {
		t.Fatalf("slash-loaded session current marker count = %d, want 1; view=%q", got, stripANSI(m.View()))
	}
}

func TestIntegrationSessionsSidebarMouseDoubleClickLoadsPersistedSession(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()

	m := newTUIModel(sess, res, true)
	m.mode, m.ready, m.width, m.height = modeChat, true, 100, 40
	m.sessions = infos
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}

	// The first saved-session row follows the title, new-session action, and divider.
	m.Update(tea.MouseMsg{Type: tea.MouseLeft, X: 1, Y: 3})
	m.Update(tea.MouseMsg{Type: tea.MouseLeft, X: 1, Y: 3})

	if got := sess.MessagesCopy(); len(got) != 2 || got[0].Content != "previous question" {
		t.Fatalf("mouse double-click did not restore persisted history: %#v", got)
	}
}

func TestIntegrationSessionsSidebarNewSessionStartsFreshConversation(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()
	sess.Messages = append(sess.Messages, provider.Message{Role: provider.RoleUser, Content: "current question"})

	m := newTUIModel(sess, res, true)
	m.mode, m.ready, m.width, m.height = modeChat, true, 100, 40
	m.sessions = infos
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if got := sess.MessagesCopy(); len(got) != 0 {
		t.Fatalf("new-session row kept current history: %#v", got)
	}
	if len(m.blocks) == 0 || !strings.Contains(stripANSI(m.blocks[len(m.blocks)-1].Text), "new session started") {
		t.Fatalf("new-session result was not visible: %#v", m.blocks)
	}
}

func TestIntegrationSessionsSidebarNewSessionBlocksWhileBusy(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()
	sess.Messages = append(sess.Messages, provider.Message{Role: provider.RoleUser, Content: "current question"})

	m := newTUIModel(sess, res, true)
	m.mode, m.ready, m.width, m.height = modeChat, true, 100, 40
	m.sessions, m.waiting = infos, true
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if got := sess.MessagesCopy(); len(got) != 1 || got[0].Content != "current question" {
		t.Fatalf("busy new-session row changed history: %#v", got)
	}
	if len(m.blocks) == 0 || !strings.Contains(stripANSI(m.blocks[len(m.blocks)-1].Text), "finish the current turn") {
		t.Fatalf("busy new-session feedback was not visible: %#v", m.blocks)
	}
}

func TestIntegrationSessionsSidebarNewSessionBlocksDuringCancellation(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()
	sess.Messages = append(sess.Messages, provider.Message{Role: provider.RoleUser, Content: "current question"})

	m := newTUIModel(sess, res, true)
	m.mode, m.ready, m.width, m.height = modeChat, true, 100, 40
	m.sessions, m.cancelling = infos, true
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if got := sess.MessagesCopy(); len(got) != 1 || got[0].Content != "current question" {
		t.Fatalf("cancelling new-session row changed history: %#v", got)
	}
}

func TestIntegrationSplashEnterSurfacesLoadFailure(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()
	if err := sess.DeleteSession("previous"); err != nil {
		t.Fatalf("delete persisted session: %v", err)
	}

	m := newTUIModel(sess, res, true)
	m.mode = modeWelcome
	m.ready = true
	m.width, m.height = 100, 40
	m.sessions = infos // stale splash row, matching an external deletion
	m.sessionSel = 0

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.mode != modeWelcome {
		t.Fatalf("failed splash load changed mode=%v", m.mode)
	}
	if !strings.Contains(m.welcomeNotice, "open failed") {
		t.Fatalf("splash load failure was not surfaced: %q", m.welcomeNotice)
	}
	if !strings.Contains(stripANSI(m.View()), "open failed") {
		t.Fatalf("splash load failure was not visible: %q", stripANSI(m.View()))
	}
}

func TestIntegrationSessionsSidebarRefreshesStaleRows(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()
	if err := sess.DeleteSession("previous"); err != nil {
		t.Fatalf("delete persisted session: %v", err)
	}

	m := newTUIModel(sess, res, true)
	m.mode = modeChat
	m.ready = true
	m.width, m.height = 100, 40
	m.sessions = infos // stale in-memory list, while the store is empty
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}

	if len(m.sessions) != 0 || m.sessionsSidebar == nil {
		t.Fatalf("stale session rows survived refresh: model=%d sidebar=%v", len(m.sessions), m.sessionsSidebar != nil)
	}
}

func TestIntegrationSessionsSlashTogglesFocusedSidebar(t *testing.T) {
	m := newReadyChatModel(30, 80)
	m.mode = modeChat

	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}
	if m.sessionsSidebar == nil {
		t.Fatal("/sessions did not open the sessions sidebar")
	}
	if m.focus != focusSidebar {
		t.Fatalf("focus = %v, want %v", m.focus, focusSidebar)
	}
	if !m.handleSlash("/sessions") {
		t.Fatal("second /sessions was not handled")
	}
	if m.sessionsSidebar != nil {
		t.Fatal("second /sessions did not close the sessions sidebar")
	}
}

func TestIntegrationSidebarEscapeCloses(t *testing.T) {
	m := newReadyChatModel(100, 40)
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.sessionsSidebar != nil {
		t.Fatal("escape did not close the sidebar")
	}
}

func TestIntegrationSidebarEnterBlocksCancelUnwind(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()
	m := newTUIModel(sess, res, true)
	m.mode, m.ready, m.width, m.height = modeChat, true, 100, 40
	m.sessions = infos
	m.cancelling = true
	before := sess.MessagesCopy()
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := sess.MessagesCopy(); len(got) != len(before) {
		t.Fatalf("sidebar loaded a session during cancellation unwind: %#v", got)
	}
}

func TestIntegrationSidebarDeleteConfirmsAndRemovesSession(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()
	m := newTUIModel(sess, res, true)
	m.mode, m.ready, m.width, m.height = modeChat, true, 100, 40
	m.sessions = infos
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(m.sessions) != 0 {
		t.Fatalf("sessions after delete = %#v", m.sessions)
	}
	if sessions, err := sess.ListSessions(); err != nil || len(sessions) != 0 {
		t.Fatalf("stored sessions after delete = %#v, %v", sessions, err)
	}
}

func TestIntegrationSidebarBlocksDestructiveActionsWhileBusy(t *testing.T) {
	sess, res, infos, cleanup := persistedSessionForSelection(t)
	defer cleanup()
	m := newTUIModel(sess, res, true)
	m.mode, m.ready, m.width, m.height = modeChat, true, 100, 40
	m.sessions = infos
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.waiting = true
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if m.sessionsSidebar.confirm != confirmNone {
		t.Fatal("d armed a delete while a turn was running")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})
	if m.sessionsSidebar.confirm != confirmNone {
		t.Fatal("P armed a purge while a turn was running")
	}
	m.waiting = false
	m.sessionsSidebar.confirm = confirmDeleteOne
	m.cancelling = true
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.sessionsSidebar.confirm != confirmDeleteOne {
		t.Fatal("confirmation changed during cancellation unwind")
	}
	if sessions, err := sess.ListSessions(); err != nil || len(sessions) != 1 {
		t.Fatalf("busy sidebar action changed stored sessions: %#v, %v", sessions, err)
	}
}

func TestIntegrationSessionStoreSlashRefreshesSidebarRows(t *testing.T) {
	sess, res, _, cleanup := persistedSessionForSelection(t)
	defer cleanup()
	m := newTUIModel(sess, res, true)
	m.mode, m.ready, m.width, m.height = modeChat, true, 100, 40
	m.sessions = nil
	if !m.handleSlash("/sessions") {
		t.Fatal("/sessions was not handled")
	}

	m.handleTuiSessionStoreSlash("/save", []string{"/save", "from-slash"})
	if len(m.sessions) != 2 || m.sessions[0].Name != "from-slash" {
		t.Fatalf("sidebar rows after save = %#v, want refreshed rows with from-slash first", m.sessions)
	}
	m.handleTuiSessionStoreSlash("/delete", []string{"/delete", "from-slash"})
	if len(m.sessions) != 1 || m.sessions[0].Name != "previous" {
		t.Fatalf("sidebar rows after delete = %#v, want only previous", m.sessions)
	}
}
