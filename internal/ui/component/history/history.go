// Package history renders the message history overlay above the composer
// and lets the user navigate and select previous prompt messages.
package history

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// maxVisibleRows is the maximum number of history items shown at once.
const maxVisibleRows = 4

// Model manages prompt message history and the selection overlay.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	// items stores messages in chronological order (oldest at index 0, newest at len-1).
	items []string

	// cursor is the index in items of the currently highlighted message.
	cursor int

	// offset is the top visible index in the windowed view.
	offset int

	// active reports whether the history overlay is currently open.
	active bool

	// width is the terminal/pane width.
	width int
}

// New returns an empty history model.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier}
}

// Len returns the count of stored history items.
func (m Model) Len() int {
	return len(m.items)
}

// Active reports whether the overlay is currently open.
func (m Model) Active() bool {
	return m.active && len(m.items) > 0
}

// SetWidth updates the available width for framing and truncation.
func (m *Model) SetWidth(w int) {
	m.width = w
}

// Push appends a new message to history, deduplicating consecutive identical entries.
func (m *Model) Push(msg string) {
	trimmed := strings.TrimSpace(msg)
	if trimmed == "" {
		return
	}
	if len(m.items) > 0 && m.items[len(m.items)-1] == msg {
		return
	}
	m.items = append(m.items, msg)
}

// SetHistory loads prior messages from conversation history.
func (m *Model) SetHistory(msgs []string) {
	m.items = nil
	for _, msg := range msgs {
		m.Push(msg)
	}
}

// Open activates the overlay, placing the cursor on the most recent message.
func (m *Model) Open() {
	if len(m.items) == 0 {
		return
	}
	m.active = true
	m.cursor = len(m.items) - 1
	m.adjustOffset()
}

// Close dismisses the overlay without selection.
func (m *Model) Close() {
	m.active = false
}

// Selected returns the text of the currently highlighted message.
func (m Model) Selected() string {
	if len(m.items) == 0 || m.cursor < 0 || m.cursor >= len(m.items) {
		return ""
	}
	return m.items[m.cursor]
}

// Up moves the selection to an older message (towards index 0).
func (m *Model) Up() {
	if !m.Active() || m.cursor <= 0 {
		return
	}
	m.cursor--
	m.adjustOffset()
}

// Down moves the selection to a newer message (towards len-1).
func (m *Model) Down() {
	if !m.Active() || m.cursor >= len(m.items)-1 {
		return
	}
	m.cursor++
	m.adjustOffset()
}

func (m *Model) adjustOffset() {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+maxVisibleRows {
		m.offset = m.cursor - maxVisibleRows + 1
	}
	maxOffset := len(m.items) - maxVisibleRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// Height returns the total number of terminal rows claimed by the framed overlay.
func (m Model) Height() int {
	if !m.Active() {
		return 0
	}
	visible := min(len(m.items), maxVisibleRows)
	return visible + 2 // 2 rows for top & bottom border frame
}

// View renders the history overlay box.
func (m Model) View() string {
	if !m.Active() {
		return ""
	}

	visibleCount := min(len(m.items), maxVisibleRows)
	end := min(m.offset+visibleCount, len(m.items))

	accent := render.Role(m.Theme, m.Tier, theme.RoleAccent)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)
	muted := render.Role(m.Theme, m.Tier, theme.RoleFGMuted)

	badge := "◷ History"
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		badge = "History"
	}
	label := fmt.Sprintf("[ %s (%d)  •  ↑/↓: navigate  •  Enter: select  •  Esc: cancel ]", badge, len(m.items))
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		label = fmt.Sprintf("[ %s (%d)  •  Up/Down: navigate  •  Enter: select  •  Esc: cancel ]", badge, len(m.items))
	}
	if !render.HintFits(m.width, label) {
		label = fmt.Sprintf("[ %s (%d) ]", badge, len(m.items))
	}

	innerWidth := m.width - 4
	if innerWidth < 10 {
		innerWidth = 10
	}

	var rows []string
	for i := m.offset; i < end; i++ {
		raw := m.items[i]
		// Collapse multi-line to a single line preview
		preview := strings.ReplaceAll(raw, "\n", " ⏎ ")
		preview = strings.TrimSpace(preview)

		prefix := "  "
		style := fg
		if i == m.cursor {
			prefix = accent.Render("● ")
			style = render.Role(m.Theme, m.Tier, theme.RoleFG).Bold(true)
		} else {
			prefix = muted.Render("○ ")
		}

		num := subtle.Render(fmt.Sprintf("%d. ", i+1))
		prefixWidth := ansi.StringWidth(prefix) + ansi.StringWidth(num)
		available := innerWidth - prefixWidth
		if available > 0 && ansi.StringWidth(preview) > available {
			preview = ansi.Truncate(preview, available, "…")
		}

		rowText := prefix + num + style.Render(preview)
		rows = append(rows, rowText)
	}

	body := strings.Join(rows, "\n")
	body = render.FillBG(m.Theme, m.Tier, theme.RoleBGSubtle, body)
	return render.BorderedWithHint(m.Theme, m.Tier, theme.RoleBorder, theme.RoleAccent, m.width, body, label)
}
