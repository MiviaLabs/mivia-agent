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
	sess := newTestSessionForModel("test-model")
	sess.Completer = welcomeStubCompleter{}
	m := &tuiModel{
		session:   sess,
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

// TestWelcomeCtrlCQuits verifies that Ctrl+C on the welcome screen produces
// tea.Quit immediately (regression: previously Ctrl+C was swallowed by the
// textarea widget as copy-to-clipboard instead of quitting).
func TestWelcomeCtrlCQuits(t *testing.T) {
	m := welcomeModel(t)
	if m.mode != modeWelcome {
		t.Fatalf("mode=%v want welcome", m.mode)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C on welcome screen must produce a command, got nil")
	}
	if !cmdsContainQuit([]tea.Cmd{cmd}) {
		t.Fatalf("Ctrl+C on welcome screen must produce tea.Quit, got cmd=%v", cmd)
	}
}

// TestWelcomeCtrlDDoesNotQuit verifies Ctrl+D is inert on the welcome screen,
// consistent with chat mode. The binding was removed because it sat next to
// ctrl+u's half-page scroll, so reaching for the neighbouring key exited mivia.
// Quitting is ctrl+c, /exit, exit or quit.
func TestWelcomeCtrlDDoesNotQuit(t *testing.T) {
	m := welcomeModel(t)
	if m.mode != modeWelcome {
		t.Fatalf("mode=%v want welcome", m.mode)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd != nil && cmdsContainQuit([]tea.Cmd{cmd}) {
		t.Fatal("Ctrl+D on the welcome screen must not quit")
	}

	// ctrl+c must still quit, or there is no keyboard exit.
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || !cmdsContainQuit([]tea.Cmd{cmd}) {
		t.Fatal("Ctrl+C must still quit the welcome screen")
	}
}

// TestWelcomeExitWordQuits verifies that typing "exit" and pressing Enter on
// the welcome screen quits the program (text-based quit path).
func TestWelcomeExitWordQuits(t *testing.T) {
	m := welcomeModel(t)
	for _, r := range []string{"e", "x", "i", "t"} {
		m.Update(keyRunes(r))
	}
	if m.textarea.Value() != "exit" {
		t.Fatalf("textarea=%q want %q", m.textarea.Value(), "exit")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("\"exit\"+Enter on welcome screen must produce a command, got nil")
	}
	if !cmdsContainQuit([]tea.Cmd{cmd}) {
		t.Fatalf("\"exit\"+Enter on welcome screen must produce tea.Quit, got cmd=%v", cmd)
	}
}

// TestWelcomeQuitWordQuits verifies that typing "quit" and pressing Enter on
// the welcome screen quits the program (text-based quit path, variant).
func TestWelcomeQuitWordQuits(t *testing.T) {
	m := welcomeModel(t)
	for _, r := range []string{"q", "u", "i", "t"} {
		m.Update(keyRunes(r))
	}
	if m.textarea.Value() != "quit" {
		t.Fatalf("textarea=%q want %q", m.textarea.Value(), "quit")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("\"quit\"+Enter on welcome screen must produce a command, got nil")
	}
	if !cmdsContainQuit([]tea.Cmd{cmd}) {
		t.Fatalf("\"quit\"+Enter on welcome screen must produce tea.Quit, got cmd=%v", cmd)
	}
}

// TestWelcomeCtrlCComposerNotEmptyStillQuits verifies that Ctrl+C quits even
// when the composer has text in it (the fix must work regardless of composer
// state, since it returns early before handleWelcomeKey or textarea.Update).
func TestWelcomeCtrlCComposerNotEmptyStillQuits(t *testing.T) {
	m := welcomeModel(t)
	for _, r := range []string{"s", "o", "m", "e"} {
		m.Update(keyRunes(r))
	}
	if m.textarea.Value() != "some" {
		t.Fatalf("textarea=%q want %q", m.textarea.Value(), "some")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C on welcome screen with text must produce a command, got nil")
	}
	if !cmdsContainQuit([]tea.Cmd{cmd}) {
		t.Fatalf("Ctrl+C on welcome screen with text must produce tea.Quit, got cmd=%v", cmd)
	}
}

// TestWelcomeModeStaysWelcomeAfterNonQuitKey verifies that normal navigation
// keys do NOT produce tea.Quit — important to ensure the fix does not break
// the existing welcome-screen behavior. (The textarea may return cursor blink
// commands; we check that no tea.Quit command is produced.)
func TestWelcomeModeStaysWelcomeAfterNonQuitKey(t *testing.T) {
	m := welcomeModel(t)

	// Up navigation should not quit.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if cmdsContainQuit([]tea.Cmd{cmd}) {
		t.Fatalf("up key must not produce tea.Quit, got cmd=%v", cmd)
	}
	if m.mode != modeWelcome {
		t.Fatalf("up key must not leave welcome, mode=%v", m.mode)
	}

	// Down navigation should not quit.
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmdsContainQuit([]tea.Cmd{cmd}) {
		t.Fatalf("down key must not produce tea.Quit, got cmd=%v", cmd)
	}
	if m.mode != modeWelcome {
		t.Fatalf("down key must not leave welcome, mode=%v", m.mode)
	}

	// Typing should not quit.
	for _, r := range []string{"t", "e", "x", "t"} {
		_, cmd = m.Update(keyRunes(r))
	}
	if cmdsContainQuit([]tea.Cmd{cmd}) {
		t.Fatalf("typing must not produce tea.Quit, got cmd=%v", cmd)
	}
	if m.mode != modeWelcome {
		t.Fatalf("typing must not leave welcome, mode=%v", m.mode)
	}
}
