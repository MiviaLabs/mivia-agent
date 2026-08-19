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

// promptWidth is the display width of the accent prompt View renders
// ahead of the input ("> ").
const promptWidth = 2

// New returns a focused, empty composer sized to width, where width is
// the full column count including the prompt.
func New(t theme.Theme, tier theme.Tier, width int) Model {
	ti := textinput.New()
	ti.Prompt = "" // the theme-styled accent prompt is rendered by this package's View, not textinput's own
	ti.Focus()
	m := Model{Theme: t, Tier: tier, input: ti}
	m.SetWidth(width)
	return m
}

// SetSuggestions sets the slash-completion candidate list.
func (m *Model) SetSuggestions(cmds []string) { m.input.SetSuggestions(cmds) }

// SetWidth resizes the input line. The prompt this package renders sits
// outside textinput's own width, so the caller passes the full column
// count and this subtracts the prompt.
func (m *Model) SetWidth(width int) {
	w := width - promptWidth
	if w < 1 {
		w = 1
	}
	m.input.SetWidth(w)
}

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
