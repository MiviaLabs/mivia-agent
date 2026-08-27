// Package queue renders the queued messages overlay above the composer
// and lets the user inspect, navigate, and remove queued messages.
package queue

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// maxVisibleRows is the maximum number of queued items shown at once.
const maxVisibleRows = 4

// Model manages queued messages and the overlay presentation.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	// items is the slice of queued message strings.
	items []string

	// cursor is the index of the currently highlighted queued item.
	cursor int

	// offset is the top visible index in the windowed view.
	offset int

	// active reports whether the queue overlay is currently open.
	active bool

	// width is the available display width.
	width int
}

// New returns an empty queue model.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier}
}

// Active reports whether the overlay is currently open.
func (m Model) Active() bool {
	return m.active
}

// Len returns the count of items in the queue.
func (m Model) Len() int {
	return len(m.items)
}

// SetWidth updates the available width for framing and truncation.
func (m *Model) SetWidth(w int) {
	m.width = w
}

// SetItems syncs the queue overlay items with the screen queue slice.
func (m *Model) SetItems(items []string) {
	m.items = items
	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
	m.adjustOffset()
}

// Items returns a copy of the current queue items.
func (m Model) Items() []string {
	out := make([]string, len(m.items))
	copy(out, m.items)
	return out
}

// Open activates the overlay.
func (m *Model) Open(items []string) {
	m.items = items
	m.active = true
	m.cursor = 0
	m.offset = 0
	m.adjustOffset()
}

// Close dismisses the overlay.
func (m *Model) Close() {
	m.active = false
}

// Up moves the selection to the previous queued item.
func (m *Model) Up() {
	if !m.active || m.cursor <= 0 {
		return
	}
	m.cursor--
	m.adjustOffset()
}

// Down moves the selection to the next queued item.
func (m *Model) Down() {
	if !m.active || m.cursor >= len(m.items)-1 {
		return
	}
	m.cursor++
	m.adjustOffset()
}

// DeleteSelected removes the currently highlighted item from the queue and returns it.
func (m *Model) DeleteSelected() (string, bool) {
	if len(m.items) == 0 || m.cursor < 0 || m.cursor >= len(m.items) {
		return "", false
	}
	removed := m.items[m.cursor]
	m.items = append(m.items[:m.cursor], m.items[m.cursor+1:]...)
	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
	m.adjustOffset()
	return removed, true
}

func (m *Model) adjustOffset() {
	if len(m.items) == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
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

// Height returns the total number of terminal rows claimed by the overlay.
func (m Model) Height() int {
	if !m.active {
		return 0
	}
	if len(m.items) == 0 {
		return 3 // 1 empty row + 2 border rows
	}
	visible := min(len(m.items), maxVisibleRows)
	return visible + 2 // visible rows + 2 border rows
}

// View renders the queued messages overlay box.
func (m Model) View() string {
	if !m.active {
		return ""
	}

	accent := render.Role(m.Theme, m.Tier, theme.RoleAccent)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)
	muted := render.Role(m.Theme, m.Tier, theme.RoleFGMuted)

	badge := "◷ Queue"
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		badge = "Queue"
	}

	label := fmt.Sprintf("[ %s (%d)  •  ↑/↓: navigate  •  d/x: remove  •  Esc: close ]", badge, len(m.items))
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		label = fmt.Sprintf("[ %s (%d)  •  Up/Down: navigate  •  d/x: remove  •  Esc: close ]", badge, len(m.items))
	}
	if len(m.items) == 0 {
		label = fmt.Sprintf("[ %s (empty)  •  Esc: close ]", badge)
	}
	if !render.HintFits(m.width, label) {
		label = fmt.Sprintf("[ %s (%d) ]", badge, len(m.items))
		if len(m.items) == 0 {
			label = fmt.Sprintf("[ %s (empty) ]", badge)
		}
	}

	innerWidth := m.width - 4
	if innerWidth < 10 {
		innerWidth = 10
	}

	var rows []string
	if len(m.items) == 0 {
		emptyText := muted.Render("No queued messages.")
		rows = append(rows, "  "+emptyText)
	} else {
		visibleCount := min(len(m.items), maxVisibleRows)
		end := min(m.offset+visibleCount, len(m.items))

		for i := m.offset; i < end; i++ {
			raw := m.items[i]
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

			num := subtle.Render(fmt.Sprintf("[%d] ", i+1))
			prefixWidth := ansi.StringWidth(prefix) + ansi.StringWidth(num)
			available := innerWidth - prefixWidth
			if available > 0 && ansi.StringWidth(preview) > available {
				preview = ansi.Truncate(preview, available, "…")
			}

			rowText := prefix + num + style.Render(preview)
			rows = append(rows, rowText)
		}
	}

	body := strings.Join(rows, "\n")
	body = render.FillBG(m.Theme, m.Tier, theme.RoleBGSubtle, body)
	return render.BorderedWithHint(m.Theme, m.Tier, theme.RoleBorder, theme.RoleAccent, m.width, body, label)
}
