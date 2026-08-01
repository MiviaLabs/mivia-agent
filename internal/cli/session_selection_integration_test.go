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
	if err := configureChatWorkspace(sess, root, true, "", config.ToolsConfig{}); err != nil {
		t.Fatalf("configure workspace: %v", err)
	}
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		return buildModelBinding(sess, res, root, providerName, model, agentSessionContext{AllowProjectSkills: true})
	})
	cleanup, err := attachSessionDispatcher(sess, root, res.Model, res.Subagents, &agentSessionState{AllowProjectSkills: true}, nil, sessionRouting{})
	if err != nil {
		t.Fatalf("attach dispatcher: %v", err)
	}
	sess.SessionDir = workspace.SessionsDir(root)
	store, err := chat.NewFileSessionStore(sess.SessionDir)
	if err != nil {
		cleanup()
		t.Fatalf("session store: %v", err)
	}
	sess.SetSessionStore(store, chat.NewSaveManager(store, res.Model, comp.Name()))
	sess.Messages = append(sess.Messages,
		provider.Message{Role: provider.RoleUser, Content: "previous question"},
		provider.Message{Role: provider.RoleAssistant, Content: "previous answer"},
	)
	if err := sess.Save("previous"); err != nil {
		cleanup()
		t.Fatalf("save session: %v", err)
	}
	infos, err := sess.ListSessions()
	if err != nil {
		cleanup()
		t.Fatalf("list sessions: %v", err)
	}
	_ = sess.Clear()
	return sess, res, infos, cleanup
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

func TestIntegrationSessionsDialogEnterLoadsPersistedSession(t *testing.T) {
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

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.sessionsDlg != nil {
		t.Fatalf("Enter did not close the sessions dialog; view=%q", stripANSI(m.View()))
	}
	if got := sess.MessagesCopy(); len(got) != 2 || got[0].Content != "previous question" {
		t.Fatalf("dialog Enter did not restore persisted history: %#v; view=%q", got, stripANSI(m.View()))
	}

	// Reopening the same saved session must rebuild a second dispatcher
	// generation without re-registering generation-owned tools.
	if !m.handleSlash("/sessions") {
		t.Fatal("second /sessions was not handled")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.sessionsDlg != nil {
		t.Fatalf("second Enter did not close the sessions dialog; view=%q", stripANSI(m.View()))
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

func TestIntegrationSessionsDialogRefreshesStaleRows(t *testing.T) {
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

	if len(m.sessions) != 0 || len(m.sessionsDlg.sessions) != 0 {
		t.Fatalf("stale session rows survived refresh: model=%d dialog=%d", len(m.sessions), len(m.sessionsDlg.sessions))
	}
}
