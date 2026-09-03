// Package composer is the multi-line message input plus a slash-command
// completion list and an @-mention file picker.
package composer

import (
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// Model wraps bubbles/v2 textarea for the multi-line input, the
// slash-completion menu, and the @-mention picker.
//
// Dynamic height: the textarea grows from 1 line to maxInputLines as the
// user types, and shrinks when lines are removed. The body has no border,
// only a solid fill; the key-hint row below it always occupies a fixed
// gutter row of its own so the rest of the cockpit layout never reflows
// (ux-rules.md rule 2.7, 2.8).
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier
	input textarea.Model
	menu  menu
	mmenu mentionMenu

	// width is the full column count the composer occupies. View renders
	// exactly this many columns on every row.
	width int

	// Mouse selection (selection.go): body rect injected by the owning
	// screen, the anchor/focus pair the router drives during a drag, and
	// the input value the selection was armed against (a later edit
	// invalidates it).
	selRect  sel.Rect
	selState sel.Selection
	selValue string
}

// maxInputLines is the maximum number of visible textarea rows before it
// scrolls internally rather than growing the frame further.
const maxInputLines = 6

// promptWidth is the display width of the accent prompt rendered by this
// package. Two columns: one for the glyph, one for the space.
const promptWidth = 2

// promptGlyph is the prompt drawn on the first input row: "› " on tiers
// that can show it, the ASCII "> " otherwise. selectionRows uses the same
// glyph so copied text matches what View draws.
func promptGlyph(tier theme.Tier) string {
	if tier == theme.TierASCII || tier == theme.TierNoTTY {
		return "> "
	}
	return "› "
}

// padInset is the total column overhead the padding removes from the inner
// textarea width: two columns each side of the filled bar. It is the same
// four columns the old rounded border plus its inner padding used to take,
// so the geometry the owning screen relies on is unchanged - only the
// border characters are gone, replaced by fill.
const padInset = 4

// padCols is the padding on each side of the bar (padInset / 2).
const padCols = 2

// minPaddedWidth is the narrowest terminal that can still hold the padding,
// prompt, cursor, and one text cell. Below it, View draws the bare filled
// body with no padding rows or columns at all.
const minPaddedWidth = 8

// New returns a focused, empty composer sized to width.
func New(t theme.Theme, tier theme.Tier, width int) Model {
	ta := newTextarea(t, tier)
	m := Model{input: ta, mmenu: mentionMenu{triggerPos: -1}}
	m.SetTheme(t, tier)
	m.SetWidth(width)
	return m
}

// newTextarea initialises a textarea.Model with the settings this composer
// requires: dynamic height, no line numbers, custom keymap (enter submits —
// the textarea only inserts newlines on ctrl+j).
func newTextarea(t theme.Theme, tier theme.Tier) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "Ask a question, describe a change, or type / for commands..."

	// Dynamic height: grows from 1 row to maxInputLines, then scrolls.
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = maxInputLines
	ta.ShowLineNumbers = false

	// Remove the border that textarea draws by default; the composer fills
	// its own background instead (render.FillBG in View), with no frame.
	ta.SetStyles(noopStyles(ta.Styles()))

	// Rebind InsertNewline to shift+enter and alt+enter.
	// Plain "enter" is reserved for IDSend in the keymap; the screen handles it
	// before forwarding events to the composer (see keys.go IDSend).
	km := ta.KeyMap
	km.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter"),
		key.WithHelp("shift+enter", "newline"),
	)
	ta.KeyMap = km

	_ = ta.Focus()
	return ta
}

// noopStyles returns styles with the built-in textarea border stripped so we
// can draw our own frame.  Prompt is set to two spaces as a placeholder; the
// real prompt is injected via SetPromptFunc after theme is applied.
func noopStyles(s textarea.Styles) textarea.Styles {
	blank := lipgloss.NewStyle()
	s.Focused.Base = blank
	s.Focused.CursorLine = blank
	s.Blurred.Base = blank
	s.Blurred.CursorLine = blank
	return s
}

// SetTheme adopts a new theme and restyles the embedded textarea.
func (m *Model) SetTheme(t theme.Theme, tier theme.Tier) {
	m.Theme, m.Tier = t, tier

	s := m.input.Styles()
	fg := render.Role(t, tier, theme.RoleFG)
	fgSubtle := render.Role(t, tier, theme.RoleFGSubtle)

	s.Focused.Text = fg
	s.Blurred.Text = render.Role(t, tier, theme.RoleFGMuted)
	s.Focused.Placeholder = fgSubtle
	s.Blurred.Placeholder = fgSubtle

	if ac := t.Resolve(theme.RoleAccent, tier); ac.Hex != "" {
		s.Focused.CursorLine = lipgloss.NewStyle().Foreground(lipgloss.Color(ac.Hex))
	} else if ac.ANSI16 >= 0 {
		s.Focused.CursorLine = lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(ac.ANSI16)))
	}
	m.input.SetStyles(s)

	// Prompt: themed accent prompt on the first line, blank indent on
	// continuation lines.
	prompt := render.Role(t, tier, theme.RoleAccent).Render(promptGlyph(tier))
	cont := strings.Repeat(" ", promptWidth)
	m.input.SetPromptFunc(promptWidth, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return prompt
		}
		return cont
	})
}

// SetCommands sets the slash-completion candidate list.
func (m *Model) SetCommands(cmds []Command) {
	m.menu.all = slices.Clone(cmds)
	m.menu.refresh(m.input.Value())
}

// Commands returns the active slash-command candidate list.
func (m Model) Commands() []Command {
	out := make([]Command, len(m.menu.all))
	copy(out, m.menu.all)
	return out
}

// SetMentions sets the workspace-entity candidate list for the @-picker.
// The caller (the conversation screen or demo harness) builds this list; the
// composer holds no filesystem access.
func (m *Model) SetMentions(mentions []Mention) {
	m.mmenu.all = slices.Clone(mentions)
	m.mmenu.refresh(m.input.Value(), m.input.Column())
}

// Mentions returns the active @-mention candidate list.
func (m Model) Mentions() []Mention {
	out := make([]Mention, len(m.mmenu.all))
	copy(out, m.mmenu.all)
	return out
}

// MenuActive reports whether the slash-command completion menu is showing.
func (m Model) MenuActive() bool { return m.menu.active && len(m.menu.matches) > 0 }

// MentionMenuActive reports whether the @-mention picker is showing.
func (m Model) MentionMenuActive() bool { return m.mmenu.active && len(m.mmenu.matches) > 0 }

// MenuNext / MenuPrev move the highlighted row in the slash menu.
func (m Model) MenuNext() Model { m.menu.next(); return m }
func (m Model) MenuPrev() Model { m.menu.prev(); return m }

// MentionMenuNext / MentionMenuPrev move the highlighted row in the mention picker.
func (m Model) MentionMenuNext() Model { m.mmenu.next(); return m }
func (m Model) MentionMenuPrev() Model { m.mmenu.prev(); return m }

// MenuDismiss closes the slash-command menu without changing the text.
func (m Model) MenuDismiss() Model {
	m.menu.active = false
	return m
}

// MentionMenuDismiss closes the @-mention picker without changing the text.
func (m Model) MentionMenuDismiss() Model {
	m.mmenu.active = false
	return m
}

// AcceptSelected replaces the input with the highlighted slash-command.
func (m Model) AcceptSelected() Model {
	if !m.MenuActive() {
		return m
	}
	m.input.SetValue("/" + m.menu.matches[m.menu.cursor].Name)
	m.input.CursorEnd()
	m.menu.active = false
	return m
}

// AcceptMention replaces the "@query" fragment with the selected mention path.
func (m Model) AcceptMention() Model {
	if !m.MentionMenuActive() {
		return m
	}
	cur := m.input.Column()
	text := m.input.Value()
	newText, newCursor := m.mmenu.replaceInText(text, cur)
	m.input.SetValue(newText)
	// Reposition cursor after the inserted path.
	m.input.CursorEnd()
	_ = newCursor // cursor repositioning: CursorEnd is the safe approximation for now
	m.mmenu.active = false
	m.mmenu.triggerPos = -1
	return m
}

// AcceptCommonPrefix extends the slash-command input by the longest shared prefix.
func (m Model) AcceptCommonPrefix() (Model, bool) {
	if !m.MenuActive() {
		return m, false
	}
	prefix := m.menu.commonPrefix()
	if prefix == "" || "/"+prefix == m.input.Value() {
		return m, false
	}
	m.input.SetValue("/" + prefix)
	m.input.CursorEnd()
	m.menu.refresh(m.input.Value())
	return m, true
}

// SetWidth resizes the input. The caller passes the full column count;
// the textarea gets what is left after the prompt and the bar's padding.
func (m *Model) SetWidth(width int) {
	m.width = width
	inner := width - promptWidth
	if width >= minPaddedWidth {
		inner = width - promptWidth - padInset
	}
	if inner < 1 {
		inner = 1
	}
	m.input.SetWidth(inner)
}

// Focus focuses the composer input.
func (m *Model) Focus() { _ = m.input.Focus() }

// Blur blurs the composer input.
func (m *Model) Blur() { m.input.Blur() }

// Value returns the current input text (may be multi-line).
func (m Model) Value() string { return m.input.Value() }

// SubmitText returns the text to send: strips a trailing backslash-newline
// escape used for portable multi-line entry (ux-rules.md rule 4.3).
func (m Model) SubmitText() string {
	v := m.input.Value()
	// "\<newline>" typed by pressing backslash then Enter should produce a
	// literal newline in the submitted text, not the escape sequence.
	return strings.ReplaceAll(v, "\\\n", "\n")
}

// Clear resets the input after a message is sent.
func (m *Model) Clear() {
	m.input.Reset()
	m.menu.refresh("")
	m.mmenu.refresh("", 0)
}

// SetValue replaces the input text (e.g. when a cancelled turn is restored).
func (m *Model) SetValue(s string) {
	m.input.SetValue(s)
	m.input.CursorEnd()
	m.menu.refresh(s)
	m.mmenu.refresh(s, m.input.Column())
}

// CursorLine returns the current line index (0-based) of the cursor.
func (m Model) CursorLine() int { return m.input.Line() }

// IsEmpty reports whether the input has no text or only whitespace.
func (m Model) IsEmpty() bool { return len(strings.TrimSpace(m.input.Value())) == 0 }

// ClickToColumn places the cursor under a click at display column x of the
// composer's first row. The prompt occupies the first promptWidth columns.
func (m *Model) ClickToColumn(x int) {
	if x <= promptWidth {
		m.input.CursorStart()
		return
	}
	want := x - promptWidth
	pos, width := 0, 0
	val := m.input.Value()
	if idx := strings.IndexByte(val, '\n'); idx >= 0 {
		val = val[:idx]
	}
	for _, r := range val {
		if width >= want {
			break
		}
		width += ansi.StringWidth(string(r))
		pos++
	}
	m.input.SetCursorColumn(pos)
}

// MenuClickRow accepts the completion row at rendered index row (0 = top).
// Returns false when the menu is closed or the row is out of range.
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

// Update forwards the message to the textarea and refreshes both menus.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		// The rows under a live selection just changed; a stale anchor
		// would copy the wrong text, so editing cancels the drag.
		m.invalidateSelection()
	}
	m.menu.refresh(m.input.Value())
	m.mmenu.refresh(m.input.Value(), m.input.Column())
	return m, cmd
}

// Height is the total row count View draws. It is: padding-top(1) +
// textarea-rows (dynamic 1-maxInputLines) + padding-bottom(1) +
// completion-menu-rows (when open). Below minPaddedWidth the padding is
// omitted, so height equals textarea-rows + menu-rows.
func (m Model) Height() int {
	taRows := m.input.Height()
	if taRows < 1 {
		taRows = 1
	}
	var pad int
	if m.width >= minPaddedWidth {
		pad = 2 // top + bottom padding row
	}
	base := taRows + pad
	// menu rows (slash or mention — only one open at a time)
	if v := m.activeMenuView(); v != "" {
		return base + strings.Count(v, "\n") + 1
	}
	return base
}

// MenuRows returns the row count the active completion or mention menu occupies (0 when closed).
func (m Model) MenuRows() int {
	if v := m.activeMenuView(); v != "" {
		return strings.Count(v, "\n") + 1
	}
	return 0
}

// InputRowFromBottom is how many rows above the screen's status row the top
// input line sits (for mouse routing). When padded, the bottom padding row
// is 1 row above the status row, so the input is 2 above. When bare, the
// input is 1 above.
func (m Model) InputRowFromBottom() int {
	if m.width < minPaddedWidth {
		return 1
	}
	return 2
}

// InputColumnOffset is how many display columns of left padding sit before
// the prompt. Mouse clicks subtract it to land on the input's own column space.
func (m Model) InputColumnOffset() int {
	if m.width < minPaddedWidth {
		return 0
	}
	return padCols
}

// Padded reports whether View draws the padding rows and columns around the
// body. The owning screen uses it for selection-region geometry: padding
// rows are not selectable. (This used to be Framed(); the padding occupies
// exactly the cells the border did.)
func (m Model) Padded() bool { return m.width >= minPaddedWidth }

// activeMenuView returns whichever menu is currently showing, prefer slash
// over mention when both are somehow active (cannot happen in practice).
func (m Model) activeMenuView() string {
	if v := m.menu.view(m.Theme, m.Tier, m.width); v != "" {
		return v
	}
	return m.mmenu.view(m.Theme, m.Tier, m.width)
}

// View renders the active menu above the textarea, then the textarea's
// body as a solid filled bar: one padding row above, two padding columns
// each side, one padding row below, all in the subtle card background
// (RoleBGSubtle) matching the web app. No border is drawn; the fill is
// the frame. The bottom padding row doubles as the key-hint row while a
// completion or mention menu is open, so the hint never adds a row. The
// textarea is the last block after the menu, so it never moves as the
// menu grows or shrinks (ux-rules.md rule 2.8).
func (m Model) View() string {
	body := m.input.View()
	if m.selState.Active {
		body = m.highlightBodyLines(body)
	}

	if m.width >= minPaddedWidth {
		inner := m.width - padInset
		pad := strings.Repeat(" ", padCols)
		lines := strings.Split(body, "\n")
		for i, ln := range lines {
			w := ansi.StringWidth(ln)
			if w < inner {
				ln += strings.Repeat(" ", inner-w)
			} else if w > inner {
				ln = ansi.Truncate(ln, inner, "")
			}
			lines[i] = pad + ln + pad
		}
		blank := strings.Repeat(" ", m.width)
		bottom := blank
		if hint := m.menuHint(); hint != "" {
			bottom = strings.Repeat(" ", m.width-ansi.StringWidth(hint)-padCols) +
				render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(hint) + pad
		}
		rows := append([]string{blank}, lines...)
		rows = append(rows, bottom)
		body = render.FillBG(m.Theme, m.Tier, theme.RoleBGSubtle, strings.Join(rows, "\n"))
	} else if m.width > 0 {
		lines := strings.Split(body, "\n")
		for i, ln := range lines {
			w := ansi.StringWidth(ln)
			if w < m.width {
				lines[i] = ln + strings.Repeat(" ", m.width-w)
			} else if w > m.width {
				lines[i] = ansi.Truncate(ln, m.width, "")
			}
		}
		body = render.FillBG(m.Theme, m.Tier, theme.RoleBGSubtle, strings.Join(lines, "\n"))
	}

	if v := m.activeMenuView(); v != "" {
		return v + "\n" + body
	}
	return body
}

// menuHint is the navigation hint shown in the bottom padding row while a
// completion or mention menu is open, or "" otherwise. The idle bar carries
// no hint: the placeholder already names "/" for commands, and Enter to
// send needs no reminder. Each hint has a shorter fallback for narrow bars.
func (m Model) menuHint() string {
	ascii := m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY
	var hint, short string
	switch {
	case m.MenuActive():
		hint, short = "[ ↑/↓: navigate • Tab: complete • Enter: select • Esc: dismiss ]", "[ / Commands ]"
		if ascii {
			hint = "[ Up/Down: navigate • Tab: complete • Enter: select • Esc: dismiss ]"
		}
	case m.MentionMenuActive():
		hint, short = "[ ↑/↓: navigate • Tab/Enter: insert • Esc: dismiss ]", "[ @ Mentions ]"
		if ascii {
			hint = "[ Up/Down: navigate • Tab/Enter: insert • Esc: dismiss ]"
		}
	default:
		return ""
	}
	if hintFits(m.width, hint) {
		return hint
	}
	if hintFits(m.width, short) {
		return short
	}
	return ""
}

// hintFits reports whether hint fits the bottom padding row with the
// bar's padding on both sides. Unlike render.HintFits (border-specific,
// still used by the approval prompt's box) there are no corners or bars to
// reserve space for.
func hintFits(width int, hint string) bool {
	return hint != "" && ansi.StringWidth(hint)+padInset <= width
}
