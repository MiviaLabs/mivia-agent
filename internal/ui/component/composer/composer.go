// Package composer is the single-line message input plus a slash-command
// completion list.
package composer

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Model wraps bubbles/v2 textinput for the input line itself, and owns
// the slash-completion menu. The real command source is cli/harness
// territory and is not wired yet (deferred with the bootstrap/adapter
// work); SetCommands lets any caller - tests now, the real app later -
// inject the active command set without this package knowing where it
// came from.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier
	input textinput.Model
	menu  menu

	// width is the full column count, including the prompt. View clamps
	// to it: bubbles/textinput reserves a cell for the cursor beyond the
	// width it was given, so the drawn line is one column wider than
	// asked. In the cockpit that one column wraps the bottom row and
	// pushes the whole layout up by a row.
	width int
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

// SetCommands sets the slash-completion candidate list.
func (m *Model) SetCommands(cmds []Command) {
	m.menu.all = cmds
	m.menu.refresh(m.input.Value())
}

// MenuActive reports whether the completion menu is showing. The caller
// routes keys by this: the menu claims Enter, Tab, Up, Down and Esc
// before the composer sees them (docs/design/ux-rules.md rule 5.3).
func (m Model) MenuActive() bool { return m.menu.active && len(m.menu.matches) > 0 }

// MenuNext and MenuPrev move the highlighted row.
func (m Model) MenuNext() Model { m.menu.next(); return m }
func (m Model) MenuPrev() Model { m.menu.prev(); return m }

// MenuDismiss closes the menu without changing the text.
func (m Model) MenuDismiss() Model {
	m.menu.active = false
	return m
}

// AcceptSelected replaces the input with the highlighted command and
// closes the menu. A trailing space is NOT added: appending one runs the
// command's default subcommand on the next Enter, which is a documented
// defect (rule 5.6).
func (m Model) AcceptSelected() Model {
	if !m.MenuActive() {
		return m
	}
	m.input.SetValue("/" + m.menu.matches[m.menu.cursor].Name)
	m.input.SetCursor(len(m.input.Value()))
	m.menu.active = false
	return m
}

// AcceptCommonPrefix extends the input by the longest prefix every match
// shares, and leaves the menu open. It reports false when the prefix
// adds nothing, so the caller can fall back to selecting the highlighted
// row instead of doing nothing visible.
func (m Model) AcceptCommonPrefix() (Model, bool) {
	if !m.MenuActive() {
		return m, false
	}
	prefix := m.menu.commonPrefix()
	if prefix == "" || "/"+prefix == m.input.Value() {
		return m, false
	}
	m.input.SetValue("/" + prefix)
	m.input.SetCursor(len(m.input.Value()))
	m.menu.refresh(m.input.Value())
	return m, true
}

// SetWidth resizes the input line. The prompt this package renders sits
// outside textinput's own width, so the caller passes the full column
// count and this subtracts the prompt.
func (m *Model) SetWidth(width int) {
	m.width = width
	w := width - promptWidth
	if w < 1 {
		w = 1
	}
	m.input.SetWidth(w)
}

// Value returns the current input text.
func (m Model) Value() string { return m.input.Value() }

// Clear resets the input, e.g. after a message is sent.
func (m *Model) Clear() {
	m.input.SetValue("")
	m.menu.refresh("")
}

// SetValue replaces the input text, e.g. when a cancelled turn restores
// what the user had typed.
func (m *Model) SetValue(s string) {
	m.input.SetValue(s)
	m.input.SetCursor(len(s))
	m.menu.refresh(s)
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.menu.refresh(m.input.Value())
	return m, cmd
}

// Height is the row count View draws, so the screen can reserve it.
func (m Model) Height() int {
	if v := m.menu.view(m.Theme, m.Tier); v != "" {
		return 1 + strings.Count(v, "\n") + 1
	}
	return 1
}

// View renders the completion menu ABOVE the input line. The input stays
// on the last row, so it never moves as the menu grows or shrinks
// (docs/design/ux-rules.md rule 2.8).
func (m Model) View() string {
	prompt := render.Role(m.Theme, m.Tier, theme.RoleAccent).Render("> ")
	line := prompt + m.input.View()
	if m.width > 0 && ansi.StringWidth(line) > m.width {
		line = ansi.Truncate(line, m.width, "")
	}
	if v := m.menu.view(m.Theme, m.Tier); v != "" {
		return v + "\n" + line
	}
	return line
}
