package app_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

// TestRouterComposesWithRealScreens is the cross-package integration test
// no per-package test file can be: it drives app.Model, the real
// conversation.Screen, and the real themepicker.Screen together through
// ctrl+t -> select -> back, the composition Step 7 actually introduces.
func TestRouterComposesWithRealScreens(t *testing.T) {
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var dark, light theme.Theme
	for _, th := range themes {
		switch th.Name {
		case "mivia-dark":
			dark = th
		case "mivia-light":
			light = th
		}
	}
	if dark.Name == "" || light.Name == "" {
		t.Fatal("need both mivia-dark and mivia-light embedded")
	}

	base := conversation.New(dark, theme.TierASCII, themes, replay.New(nil, 0), nil, 40, nil)
	m := app.New(base, dark, theme.TierASCII, themes)

	// Type something into the composer before opening the picker, to
	// prove the base screen's state survives the round trip.
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	m = next.(app.Model)

	// ctrl+t: the real conversation.Screen emits app.PushScreenMsg
	// carrying a real themepicker.Screen; the router must apply it.
	next, cmd := m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = next.(app.Model)
	if cmd == nil {
		t.Fatal("expected a PushScreenMsg Cmd from ctrl+t")
	}
	next, _ = m.Update(cmd())
	m = next.(app.Model)

	view := m.View()
	if !view.AltScreen {
		t.Error("expected the pushed theme picker to render full alt-screen")
	}
	if !strings.Contains(view.Content, "select a theme") || !strings.Contains(view.Content, light.Name) {
		t.Errorf("expected the real themepicker.Screen's view, got:\n%s", view.Content)
	}

	// Select mivia-light: themepicker emits app.ThemeSelectedMsg, which
	// only the router (not any Screen) is allowed to act on. Filter
	// instead of assuming a cursor position - the embedded theme order
	// is alphabetical (mivia-dark, mivia-high-contrast, mivia-light),
	// not insertion order.
	for _, r := range "light" {
		next, cmd = m.Update(tea.KeyPressMsg{Text: string(r), Code: r})
		m = next.(app.Model)
		if cmd != nil {
			t.Fatal("filtering should not itself emit a Cmd")
		}
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(app.Model)
	if cmd == nil {
		t.Fatal("expected a ThemeSelectedMsg Cmd from enter")
	}
	next, _ = m.Update(cmd())
	m = next.(app.Model)

	if m.Theme.Name != light.Name {
		t.Errorf("got theme %q, want the router to have adopted %q", m.Theme.Name, light.Name)
	}
	view = m.View()
	if view.AltScreen {
		t.Error("expected the picker popped back to the inline base screen")
	}
	if !strings.Contains(view.Content, "h") {
		t.Error("expected the composer's typed text to survive the picker round trip")
	}
}
