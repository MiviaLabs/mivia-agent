package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// welcomeStubCompleter answers immediately so startAI workers do not panic.
type welcomeStubCompleter struct{}

func (welcomeStubCompleter) Name() string { return "welcome-stub" }
func (welcomeStubCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "ok", nil
}
func (welcomeStubCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	_, _ = io.WriteString(w, "ok")
	return "ok", nil
}
func (welcomeStubCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "ok"}, nil
}

// welcomeModel is a focused welcome-screen model for key routing tests.
func welcomeModel(t *testing.T) *tuiModel {
	t.Helper()
	ti := textarea.New()
	ti.Focus()
	ti.SetWidth(80)
	ti.SetHeight(3)
	ti.CharLimit = 0
	ti.ShowLineNumbers = false
	m := &tuiModel{
		session: &chat.Session{
			Model:     "test-model",
			Completer: welcomeStubCompleter{},
		},
		modelName: "test-model",
		viewport:  viewport.New(80, 20),
		textarea:  ti,
		messages:  []string{},
		blocks:    []ChatBlock{},
		bridge:    newStreamBridge(),
		toolPanel: toolPanelState{Selected: -1},
		focus:     focusComposer,
		mode:      modeWelcome,
		sessions: []chat.SessionInfo{
			{Name: "session-a"},
			{Name: "session-b"},
			{Name: "session-c"},
		},
		sessionSel: 1,
		width:      80,
		height:     40,
		ready:      true,
	}
	m.setFocus(focusComposer)
	return m
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// TestWelcomeAcceptsTypedInput is the regression for the welcome-screen bug where
// only ↑↓ worked and the composer never received printable keys (textarea.Update
// was gated to modeChat only after the R0–R3 key refactor).
func TestWelcomeAcceptsTypedInput(t *testing.T) {
	m := welcomeModel(t)
	if m.mode != modeWelcome {
		t.Fatalf("mode=%v want welcome", m.mode)
	}

	// Type a multi-character prompt without using Enter yet.
	for _, r := range []string{"h", "e", "l", "l", "o"} {
		m.Update(keyRunes(r))
	}
	got := m.textarea.Value()
	if got != "hello" {
		t.Fatalf("welcome composer after typing = %q, want %q", got, "hello")
	}
	if m.mode != modeWelcome {
		t.Fatalf("typing must not leave welcome, mode=%v", m.mode)
	}
	// Session selection must not jump while typing printable text.
	if m.sessionSel != 1 {
		t.Fatalf("sessionSel=%d want 1 (typing must not navigate)", m.sessionSel)
	}
}

func TestWelcomeNavKeysDoNotTypeWhenComposerEmpty(t *testing.T) {
	m := welcomeModel(t)
	before := m.sessionSel

	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.sessionSel != before-1 {
		t.Fatalf("up: sessionSel=%d want %d", m.sessionSel, before-1)
	}
	if strings.TrimSpace(m.textarea.Value()) != "" {
		t.Fatalf("up must not insert into composer, got %q", m.textarea.Value())
	}

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.sessionSel != before {
		t.Fatalf("down: sessionSel=%d want %d", m.sessionSel, before)
	}
}

func TestWelcomeJKNavOnlyWhenComposerEmpty(t *testing.T) {
	m := welcomeModel(t)

	// Empty: j/k navigate sessions.
	m.Update(keyRunes("j"))
	if m.sessionSel != 2 {
		t.Fatalf("empty j: sessionSel=%d want 2", m.sessionSel)
	}
	if m.textarea.Value() != "" {
		t.Fatalf("empty j must not type, got %q", m.textarea.Value())
	}

	// Non-empty: j is a character for the new-session prompt.
	m.Update(keyRunes("x"))
	m.Update(keyRunes("j"))
	if m.textarea.Value() != "xj" {
		t.Fatalf("non-empty j must type, got %q want %q", m.textarea.Value(), "xj")
	}
	if m.sessionSel != 2 {
		t.Fatalf("non-empty j must not navigate, sessionSel=%d", m.sessionSel)
	}
}

func TestWelcomeEnterWithTextStartsNewChat(t *testing.T) {
	m := welcomeModel(t)
	for _, r := range []string{"h", "i"} {
		m.Update(keyRunes(r))
	}
	if m.textarea.Value() != "hi" {
		t.Fatalf("pre-enter textarea=%q", m.textarea.Value())
	}

	// Enter should leave welcome and begin chat (stub Completer finishes fast).
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeChat {
		t.Fatalf("enter with text: mode=%v want modeChat", m.mode)
	}
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.bridge.Close()
	m.mu.Unlock()
	m.workerWG.Wait()
}
