// Package themepicker is the alt-screen modal (build spec section 3.4)
// that live-previews and selects an app-wide theme.
package themepicker

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

var _ app.Screen = Screen{}

// previewAccentGlyph is the preview's prompt marker, styled with
// RoleAccent - the same convention the composer's own "> " prompt uses,
// so the preview reads as a small sample of real chrome rather than an
// arbitrary swatch.
const previewAccentGlyph = "> "

// previewSample is the fixed sentence the preview renders. It exists to
// show how a theme colours real content (a prompt line, plain prose),
// not to demonstrate every role: the picker is a quick before/after
// check while browsing, not a full palette reference.
const previewSample = "Add retry with backoff to the uploader."

// Screen wraps picker.Model over a theme name list. Selecting an item
// emits app.ThemeSelectedMsg; cancelling emits app.PopScreenMsg. Neither
// mutates app-level state directly - only the router does that.
type Screen struct {
	Theme  theme.Theme
	Tier   theme.Tier
	picker picker.Model

	// themes is the full list New() was given, kept so the preview can
	// look up the highlighted row's actual theme.Theme (colours, not
	// just its name) as the cursor moves - before Enter ever applies it.
	themes []theme.Theme
}

// New builds a picker over the given themes, rendered with the current
// (pre-selection) theme so the modal itself stays legible mid-pick.
func New(th theme.Theme, tier theme.Tier, themes []theme.Theme) Screen {
	names := make([]string, len(themes))
	for i, t := range themes {
		names[i] = t.Name
	}
	return Screen{Theme: th, Tier: tier, picker: picker.New(th, tier, names), themes: themes}
}

// previewTheme resolves the picker's currently highlighted row to its
// theme.Theme. It falls back to the screen's own (still-active) theme
// when nothing is highlighted, which only happens with an empty list.
func (s Screen) previewTheme() theme.Theme {
	name, ok := s.picker.Selected()
	if !ok {
		return s.Theme
	}
	for _, t := range s.themes {
		if t.Name == name {
			return t
		}
	}
	return s.Theme
}

func (s Screen) Init() tea.Cmd { return nil }

// ViewFlags holds the alternate screen: a theme preview is a cockpit
// modal, and the router re-enters the cockpit when it pops.
func (s Screen) ViewFlags() app.ViewFlags { return app.ViewFlags{AltScreen: true} }

func (s Screen) Update(msg tea.Msg) (app.Screen, tea.Cmd) {
	if changed, ok := msg.(app.ThemeChangedMsg); ok {
		s.Theme, s.Tier = changed.Theme, changed.Tier
		s.picker.Theme, s.picker.Tier = changed.Theme, changed.Tier
		return s, nil
	}
	next, cmd := s.picker.Update(msg)
	s.picker = next
	if cmd == nil {
		return s, nil
	}
	// picker.Model's Update only ever produces a non-nil Cmd for "enter"
	// (SelectMsg) or "esc" (CancelMsg) - see internal/ui/component/picker.
	// The fallthrough below is unreachable through picker's real
	// behavior today; it exists so this stays correct (drop the Msg,
	// not panic) if that vocabulary ever grows without a matching case
	// here landing first.
	switch m := cmd().(type) {
	case picker.SelectMsg:
		return s, func() tea.Msg { return app.ThemeSelectedMsg{Name: m.Item} }
	case picker.CancelMsg:
		return s, func() tea.Msg { return app.PopScreenMsg{} }
	}
	return s, nil
}

func (s Screen) View() string {
	title := render.Role(s.Theme, s.Tier, theme.RoleFG).Bold(true).Render("select a theme")
	hint := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render("[enter] select  [esc] cancel  type to filter")
	return title + "\n\n" + s.picker.View() + "\n\n" + s.previewView() + "\n\n" + hint
}

// previewView renders previewSample styled with the highlighted row's
// theme - live, as the cursor moves, not only once Enter applies it.
func (s Screen) previewView() string {
	pt := s.previewTheme()
	prompt := render.Role(pt, s.Tier, theme.RoleAccent).Render(previewAccentGlyph)
	body := render.Role(pt, s.Tier, theme.RoleFG).Render(previewSample)
	return prompt + body
}
