package app_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

// routerFixture bundles the dark/light themes a router integration test
// needs, split out so each test function stays within the repo's
// per-function line budget.
type routerFixture struct {
	dark, light theme.Theme
	themes      []theme.Theme
}

func newRouterFixture(t *testing.T) routerFixture {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var f routerFixture
	f.themes = themes
	for _, th := range themes {
		switch th.Name {
		case "mivia-dark":
			f.dark = th
		case "mivia-light":
			f.light = th
		}
	}
	if f.dark.Name == "" || f.light.Name == "" {
		t.Fatal("need both mivia-dark and mivia-light embedded")
	}
	return f
}

// openThemePicker builds a router over a real conversation.Screen, types
// "h" into the composer (to later prove state survives the round trip),
// and opens the theme picker via ctrl+t.
func openThemePicker(t *testing.T, f routerFixture) app.Model {
	t.Helper()
	base := conversation.New(f.dark, theme.TierTrueColor, f.themes, replay.New(nil, 0), nil, 40, nil)
	m := app.New(base, f.dark, theme.TierTrueColor, f.themes)

	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	m = next.(app.Model)

	next, cmd := m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = next.(app.Model)
	if cmd == nil {
		t.Fatal("expected a PushScreenMsg Cmd from ctrl+t")
	}
	next, _ = m.Update(cmd())
	return next.(app.Model)
}

// selectTheme filters the open picker down to name and confirms it.
func selectTheme(t *testing.T, m app.Model, name string) app.Model {
	t.Helper()
	var cmd tea.Cmd
	var next tea.Model
	for _, r := range name {
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
	return next.(app.Model)
}

// TestThemePickerOpensAsAltScreenModal is the cross-package integration
// test no per-package test file can be: it drives app.Model and the
// real conversation.Screen/themepicker.Screen together through ctrl+t,
// the composition Step 7 actually introduces.
func TestThemePickerOpensAsAltScreenModal(t *testing.T) {
	f := newRouterFixture(t)
	m := openThemePicker(t, f)

	view := m.View()
	if !view.AltScreen {
		t.Error("expected the pushed theme picker to render full alt-screen")
	}
	if !strings.Contains(view.Content, "select a theme") || !strings.Contains(view.Content, f.light.Name) {
		t.Errorf("expected the real themepicker.Screen's view, got:\n%s", view.Content)
	}
}

// TestSelectingThemeChangesRenderedColourAndPreservesState is the
// regression this package exists to catch: selecting a theme must
// actually reach the base screen's rendering, not just app.Model.Theme.
func TestSelectingThemeChangesRenderedColourAndPreservesState(t *testing.T) {
	f := newRouterFixture(t)
	m := openThemePicker(t, f)
	m = selectTheme(t, m, "light")

	if m.Theme.Name != f.light.Name {
		t.Errorf("got theme %q, want the router to have adopted %q", m.Theme.Name, f.light.Name)
	}
	view := m.View()
	if view.AltScreen {
		t.Error("expected the picker popped back to the inline base screen")
	}
	if !strings.Contains(view.Content, "h") {
		t.Error("expected the composer's typed text to survive the picker round trip")
	}

	// The composer's accent prompt must render in the newly-adopted
	// theme's accent colour, not the original mivia-dark's.
	wantAccent := render.Role(f.light, theme.TierTrueColor, theme.RoleAccent).Render("> ")
	if !strings.Contains(view.Content, wantAccent) {
		t.Errorf("expected the composer prompt styled with %s's accent colour, got:\n%q", f.light.Name, view.Content)
	}
	darkAccent := render.Role(f.dark, theme.TierTrueColor, theme.RoleAccent).Render("> ")
	if darkAccent != wantAccent && strings.Contains(view.Content, darkAccent) {
		t.Errorf("expected the original mivia-dark accent colour gone after switching to %s, got:\n%q", f.light.Name, view.Content)
	}
}
