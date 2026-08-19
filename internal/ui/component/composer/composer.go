// Package composer is the single-line message input plus a slash-command
// completion list.
package composer

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Model wraps bubbles/v2 textinput for the input line itself. The real
// slash-command source is cli/harness territory and is not wired yet
// (deferred with the bootstrap/adapter work); SetSuggestions lets any
// caller - tests now, the real app later - inject the active command
// set without this package knowing where it came from.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier
	input textinput.Model
}

// New returns a focused, empty composer sized to width.
func New(t theme.Theme, tier theme.Tier, width int) Model {
	ti := textinput.New()
	ti.Prompt = "" // the theme-styled accent prompt is rendered by this package's View, not textinput's own
	ti.SetWidth(width)
	ti.Focus()
	return Model{Theme: t, Tier: tier, input: ti}
}

// SetSuggestions sets the slash-completion candidate list.
func (m *Model) SetSuggestions(cmds []string) { m.input.SetSuggestions(cmds) }

// Value returns the current input text.
func (m Model) Value() string { return m.input.Value() }

// Clear resets the input, e.g. after a message is sent.
func (m *Model) Clear() { m.input.SetValue("") }

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	prompt := render.Role(m.Theme, m.Tier, theme.RoleAccent).Render("> ")
	return prompt + m.input.View()
}
