package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// stubScreen is a minimal app.Screen for router tests: it records every
// Msg it receives and can optionally return a canned Cmd.
type stubScreen struct {
	name     string
	received []tea.Msg
	cmd      tea.Cmd
	initCmd  tea.Cmd
}

func (s stubScreen) Init() tea.Cmd { return s.initCmd }

func (s stubScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	s.received = append(s.received, msg)
	return s, s.cmd
}

func (s stubScreen) View() string { return s.name }

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

func keyMsg(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Text: s, Code: rune(s[0])} }

func TestViewRendersTopOfStack(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	if got := m.View(); got.Content != "base" {
		t.Errorf("got %q, want the base screen's View()", got.Content)
	}
}

func TestViewAltScreenOnlyForModal(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	if got := m.View(); got.AltScreen {
		t.Error("expected the base screen to render inline (AltScreen false)")
	}
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal"}})
	m = next.(Model)
	if got := m.View(); !got.AltScreen {
		t.Error("expected a pushed modal to render full alt-screen")
	}
}

func TestPushScreenMsgPushesAndInits(t *testing.T) {
	initCalled := false
	modal := stubScreen{name: "modal", initCmd: func() tea.Msg { initCalled = true; return nil }}
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)

	next, cmd := m.Update(PushScreenMsg{Screen: modal})
	m = next.(Model)
	if got := m.View(); got.Content != "modal" {
		t.Fatalf("got %q, want the pushed modal on top", got.Content)
	}
	if cmd == nil {
		t.Fatal("expected the pushed screen's Init Cmd")
	}
	cmd()
	if !initCalled {
		t.Error("expected the pushed screen's Init to have been returned and callable")
	}
}

func TestEscPopsModalNotBase(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)

	// Esc on the base screen alone must not pop (nothing to pop) and
	// must reach the base screen's own Update instead.
	next, _ := m.Update(keyMsg("esc"))
	m = next.(Model)
	base := m.stack[0].(stubScreen)
	if len(base.received) != 1 {
		t.Fatalf("expected esc forwarded to the lone base screen, got %d received msgs", len(base.received))
	}

	next, _ = m.Update(PushScreenMsg{Screen: stubScreen{name: "modal"}})
	m = next.(Model)
	if got := m.View(); got.Content != "modal" {
		t.Fatalf("got %q, want modal pushed", got.Content)
	}

	next, _ = m.Update(keyMsg("esc"))
	m = next.(Model)
	if got := m.View(); got.Content != "base" {
		t.Errorf("got %q, want esc to pop the modal back to base", got.Content)
	}
}

func TestPopScreenMsgOnBaseIsNoOp(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PopScreenMsg{})
	m = next.(Model)
	if got := m.View(); got.Content != "base" {
		t.Errorf("got %q, want the base screen to survive a pop with nothing above it", got.Content)
	}
}

func TestPopScreenMsgWithModalPops(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "modal"}})
	m = next.(Model)
	next, _ = m.Update(PopScreenMsg{})
	m = next.(Model)
	if got := m.View(); got.Content != "base" {
		t.Errorf("got %q, want PopScreenMsg to remove the modal", got.Content)
	}
}

func TestCtrlCQuits(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	_, cmd := m.Update(keyMsg("ctrl+c"))
	if cmd == nil {
		t.Fatal("expected a Cmd for ctrl+c")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("got %T, want tea.QuitMsg", msg)
	}
}

func TestThemeSelectedMsgAdoptsThemeAndPops(t *testing.T) {
	dark := loadTheme(t)
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var light theme.Theme
	for _, th := range themes {
		if th.Name == "mivia-light" {
			light = th
		}
	}
	if light.Name == "" {
		t.Fatal("mivia-light theme not found")
	}

	m := New(stubScreen{name: "base"}, dark, theme.TierASCII, themes)
	next, _ := m.Update(PushScreenMsg{Screen: stubScreen{name: "picker"}})
	m = next.(Model)

	next, _ = m.Update(ThemeSelectedMsg{Name: light.Name})
	m = next.(Model)

	if m.Theme.Name != light.Name {
		t.Errorf("got theme %q, want %q", m.Theme.Name, light.Name)
	}
	if got := m.View(); got.Content != "base" {
		t.Errorf("got %q, want the picker popped after selection", got.Content)
	}
}

func TestThemeSelectedMsgUnknownNameLeavesThemeUnchanged(t *testing.T) {
	dark := loadTheme(t)
	m := New(stubScreen{name: "base"}, dark, theme.TierASCII, []theme.Theme{dark})
	next, _ := m.Update(ThemeSelectedMsg{Name: "does-not-exist"})
	m = next.(Model)
	if m.Theme.Name != dark.Name {
		t.Errorf("got theme %q, want unchanged %q", m.Theme.Name, dark.Name)
	}
}

func TestUnrecognisedMsgForwardsToTopScreen(t *testing.T) {
	m := New(stubScreen{name: "base"}, loadTheme(t), theme.TierASCII, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)
	base := m.stack[0].(stubScreen)
	if len(base.received) != 1 {
		t.Fatalf("expected the Msg forwarded to the top screen, got %d received", len(base.received))
	}
	if _, ok := base.received[0].(tea.WindowSizeMsg); !ok {
		t.Errorf("got %T, want tea.WindowSizeMsg forwarded verbatim", base.received[0])
	}
}

// TestEmptyStackDefensiveBranches proves top()/Init()/View()/Update() are
// safe on a zero-value stack. New always seeds one screen, so this can
// only happen via direct construction - defensive, but a real path.
func TestEmptyStackDefensiveBranches(t *testing.T) {
	var m Model
	if cmd := m.Init(); cmd != nil {
		t.Error("expected nil Init Cmd on an empty stack")
	}
	if got := m.View(); got.Content != "" {
		t.Errorf("got %q, want empty View() on an empty stack", got.Content)
	}
	next, cmd := m.Update(keyMsg("x"))
	if cmd != nil {
		t.Error("expected no Cmd from Update on an empty stack")
	}
	if got := next.(Model).View(); got.Content != "" {
		t.Errorf("got %q, want Update to be a safe no-op on an empty stack", got.Content)
	}
}

func TestInitDelegatesToTopScreen(t *testing.T) {
	called := false
	base := stubScreen{name: "base", initCmd: func() tea.Msg { called = true; return nil }}
	m := New(base, loadTheme(t), theme.TierASCII, nil)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected Init to delegate to the base screen")
	}
	cmd()
	if !called {
		t.Error("expected the base screen's Init Cmd to be returned")
	}
}
