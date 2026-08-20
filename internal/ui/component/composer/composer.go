// Package composer is the single-line message input plus a slash-command
// completion list.
package composer

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
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

// cursorReserve is the cell bubbles/v2 textinput draws beyond the width
// it is given. Measured on this tree against bubbles v2.1.1: with the
// cursor at the end of the value, View renders the value plus a cursor
// cell plus Width()-valWidth padding (Width()+1 columns); with the
// cursor inside the value, the cursor replaces one value cell and the
// padding grows by one to compensate (still Width()+1). Passing width-
// promptWidth-cursorReserve to SetWidth therefore draws exactly `width`
// columns. This is a measured library behavior, not an off-by-one.
const cursorReserve = 1

// New returns a focused, empty composer sized to width, where width is
// the full column count including the prompt.
func New(t theme.Theme, tier theme.Tier, width int) Model {
	ti := textinput.New()
	ti.Prompt = "" // the theme-styled accent prompt is rendered by this package's View, not textinput's own
	ti.Placeholder = "Ask a question, describe a change, or type / for commands..."
	ti.Focus()
	m := Model{input: ti}
	m.SetTheme(t, tier)
	m.SetWidth(width)
	return m
}

// SetTheme adopts a new theme and restyles the embedded textinput with
// it.
//
// bubbles/textinput ships its own hard-coded default styles, so without
// this the one thing the user watches while typing - the text itself,
// and the placeholder before it - kept the library's colour whatever
// theme was active. On a light theme that is white text on a light
// surface, which reads as "selecting a theme did nothing".
//
// It is a method rather than an assignment to Theme/Tier because the
// restyle must not be forgotten: every other themed component here
// (transcript, topbar, statusline, welcome) already carries the same
// SetTheme shape.
func (m *Model) SetTheme(t theme.Theme, tier theme.Tier) {
	m.Theme, m.Tier = t, tier
	st := textinput.DefaultStyles(t.Dark)
	st.Focused.Text = render.Role(t, tier, theme.RoleFG)
	st.Focused.Placeholder = render.Role(t, tier, theme.RoleFGSubtle)
	st.Focused.Suggestion = render.Role(t, tier, theme.RoleFGSubtle)
	st.Blurred.Text = render.Role(t, tier, theme.RoleFGMuted)
	st.Blurred.Placeholder = render.Role(t, tier, theme.RoleFGSubtle)
	st.Blurred.Suggestion = render.Role(t, tier, theme.RoleFGSubtle)
	if s := t.Resolve(theme.RoleAccent, tier); s.Hex != "" {
		st.Cursor.Color = lipgloss.Color(s.Hex)
	} else if s.ANSI16 >= 0 {
		st.Cursor.Color = lipgloss.Color(strconv.Itoa(s.ANSI16))
	} else {
		// No colour at this tier: the cursor must not smuggle one in.
		st.Cursor.Color = nil
	}
	m.input.SetStyles(st)
}

// SetCommands sets the slash-completion candidate list.
func (m *Model) SetCommands(cmds []Command) {
	m.menu.all = cmds
	m.menu.refresh(m.input.Value())
}

// Commands returns the active slash-command candidate list.
func (m Model) Commands() []Command {
	out := make([]Command, len(m.menu.all))
	copy(out, m.menu.all)
	return out
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

// SetWidth resizes the input line. The caller passes the full column
// count; the prompt this package renders and the cursor cell textinput
// reserves are subtracted, so the drawn line is exactly the width given
// for every width that can hold prompt, cursor and one text cell.
func (m *Model) SetWidth(width int) {
	m.width = width
	w := width - promptWidth - cursorReserve
	if width >= minFramedWidth {
		// The frame's border cells plus the two lipgloss counts inside
		// Width() (measured on the approval prompt): the input must be
		// SIZED into the frame, never wrapped by it, or the bottom row
		// count changes under Height().
		w -= frameInset
	}
	if w < 0 {
		w = 0
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

// ClickToColumn places the cursor under a click at display column x of
// the composer's row (the row that starts with the prompt). x is the
// column the mouse reported, not offset by the prompt.
func (m *Model) ClickToColumn(x int) {
	if x <= promptWidth {
		m.input.SetCursor(0)
		return
	}
	want := x - promptWidth
	pos, width := 0, 0
	for _, r := range m.input.Value() {
		if width >= want {
			break
		}
		width += ansi.StringWidth(string(r))
		pos++
	}
	m.input.SetCursor(pos)
}

// MenuClickRow accepts the completion row at rendered index row (0 is
// the top visible row). It reports false when the menu is closed or the
// row is outside the rendered window, so the click can fall through.
func (m *Model) MenuClickRow(row int) bool {
	if !m.MenuActive() || row < 0 {
		return false
	}
	end := min(m.menu.offset+uikitconfig.MaxCompletionRows, len(m.menu.matches))
	if idx := m.menu.offset + row; idx < end {
		m.menu.cursor = idx
		*m = m.AcceptSelected()
		return true
	}
	return false
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.menu.refresh(m.input.Value())
	return m, cmd
}

// Height is the row count View draws, so the screen can reserve it.
// The framed input is three rows (top border, input, bottom border);
// the completion menu adds its rows above the frame.
func (m Model) Height() int {
	rows := 3
	if m.width < minFramedWidth {
		rows = 1
	}
	if v := m.menu.view(m.Theme, m.Tier, m.width); v != "" {
		return rows + strings.Count(v, "\n") + 1
	}
	return rows
}

// minFramedWidth is the narrowest terminal that still frames the input:
// the frame removes frameInset columns, and the remaining inner width
// must hold the prompt, the cursor cell and one text cell (8 = 4 + 4).
// Below it View degrades to the bare line rather than let the border
// widen past the terminal.
const minFramedWidth = 8

// frameInset is the column count the border removes from the input:
// both border cells and the two cells lipgloss counts inside Width().
const frameInset = 4

// InputRowFromBottom is how many rows above the screen's last row the
// input line sits: the bottom border is below it when framed, nothing
// when degraded. Mouse routing uses this instead of assuming the input
// is the last row.
func (m Model) InputRowFromBottom() int {
	if m.width < minFramedWidth {
		return 0
	}
	return 1
}

// InputColumnOffset is how many display columns the border puts before
// the prompt. Mouse clicks subtract it to land on the input's own
// column space.
func (m Model) InputColumnOffset() int {
	if m.width < minFramedWidth {
		return 0
	}
	return 1
}

// inputLine is the composer's bottom row before the width clamp: the
// theme-styled prompt plus textinput's own View. It is separate so a
// test can measure the line BEFORE the clamp and prove the clamp is a
// backstop, not the sizing mechanism.
func (m Model) inputLine() string {
	prompt := render.Role(m.Theme, m.Tier, theme.RoleAccent).Render("> ")
	return prompt + m.input.View()
}

// View renders the completion menu ABOVE the input line. The input stays
// on the last row, so it never moves as the menu grows or shrinks
// (docs/design/ux-rules.md rule 2.8).
//
// The clamp is a backstop, not the fix: SetWidth already sizes the line
// to exactly m.width. It fires only when the width cannot hold the
// prompt, the cursor cell and one text cell (width under 4), or if a
// future textinput change draws wider than documented. In the cockpit
// one overflowing column wraps the bottom row and pushes the whole
// layout up by one, which is why the backstop stays even though the
// normal path never reaches it.
func (m Model) View() string {
	line := m.inputLine()
	if m.width >= minFramedWidth && ansi.StringWidth(line) > m.width-frameInset {
		// Text longer than the frame clips to the inner width, exactly
		// like the approval prompt: a line lipgloss wraps would add a
		// row Height() does not claim.
		line = ansi.Truncate(line, m.width-frameInset, "")
	}
	if m.width >= minFramedWidth {
		hint := "[ ↵ Send  •  / Commands ]"
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			hint = "[ Enter: Send  •  / Commands ]"
		}
		if m.input.Value() != "" {
			hint = "[ ↵ Send  •  Esc Cancel ]"
			if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
				hint = "[ Enter: Send  •  Esc Cancel ]"
			}
		}
		line = render.BorderedWithHint(m.Theme, m.Tier, theme.RoleBorder, theme.RoleFGSubtle, m.width, line, hint)
	} else if m.width > 0 && ansi.StringWidth(line) > m.width {
		line = ansi.Truncate(line, m.width, "")
	}
	if v := m.menu.view(m.Theme, m.Tier, m.width); v != "" {
		return v + "\n" + line
	}
	return line
}
