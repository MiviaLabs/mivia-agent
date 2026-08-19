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

// Screen wraps picker.Model over a theme name list. Selecting an item
// emits app.ThemeSelectedMsg; cancelling emits app.PopScreenMsg. Neither
// mutates app-level state directly - only the router does that.
type Screen struct {
	Theme  theme.Theme
	Tier   theme.Tier
	picker picker.Model
}

// New builds a picker over the given themes, rendered with the current
// (pre-selection) theme so the modal itself stays legible mid-pick.
func New(th theme.Theme, tier theme.Tier, themes []theme.Theme) Screen {
	names := make([]string, len(themes))
	for i, t := range themes {
		names[i] = t.Name
	}
	return Screen{Theme: th, Tier: tier, picker: picker.New(th, tier, names)}
}

func (s Screen) Init() tea.Cmd { return nil }

func (s Screen) Update(msg tea.Msg) (app.Screen, tea.Cmd) {
	next, cmd := s.picker.Update(msg)
	s.picker = next
	if cmd == nil {
		return s, nil
	}
	switch m := cmd().(type) {
	case picker.SelectMsg:
		return s, func() tea.Msg { return app.ThemeSelectedMsg{Name: m.Item} }
	case picker.CancelMsg:
		return s, func() tea.Msg { return app.PopScreenMsg{} }
	default:
		return s, cmd
	}
}

func (s Screen) View() string {
	title := render.Role(s.Theme, s.Tier, theme.RoleFG).Bold(true).Render("select a theme")
	hint := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render("[enter] select  [esc] cancel  type to filter")
	return title + "\n\n" + s.picker.View() + "\n\n" + hint
}
