package cli

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// focusableBlockIDs returns chatBlockRanges keys in visual (line) order,
// excluding dividers and empty ranges. Work-group headers (work:*) are included.
func focusableBlockIDs(ranges map[string][2]int, blocks []ChatBlock) []string {
	if len(ranges) == 0 {
		return nil
	}
	skip := map[string]bool{}
	for _, b := range blocks {
		if b.Kind == ChatBlockDivider && b.ID != "" {
			skip[b.ID] = true
		}
	}
	type item struct {
		id    string
		start int
	}
	items := make([]item, 0, len(ranges))
	for id, r := range ranges {
		if skip[id] || r[1] <= r[0] {
			continue
		}
		items = append(items, item{id: id, start: r[0]})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].start != items[j].start {
			return items[i].start < items[j].start
		}
		return items[i].id < items[j].id
	})
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out
}

// cycleChatFocus implements Tab / Shift+Tab among bubbles when in chat history.
// Composer → first (or last if reverse) focusable; edges wrap to composer.
func (m *tuiModel) cycleChatFocus(reverse bool) bool {
	// Refresh ranges if empty but we have blocks.
	if len(m.chatBlockRanges) == 0 && len(m.blocks) > 0 {
		r := m.renderBlocksForView()
		m.chatBlockRanges = r.Ranges
		m.messages = r.Lines
	}
	ids := focusableBlockIDs(m.chatBlockRanges, m.blocks)

	if m.focus == focusComposer {
		m.setFocus(focusScrollback)
		if len(ids) == 0 {
			if m.focusLiveToolStrip(reverse) {
				return true
			}
			m.renderVP() // clear any stale chrome
			return true
		}
		if reverse {
			m.selectedBlockID = ids[len(ids)-1]
		} else {
			m.selectedBlockID = ids[0]
		}
		m.ensureSelectedVisible()
		m.renderVP()
		return true
	}

	// focusScrollback
	if m.toolPanel.Focused {
		m.leaveToolPanelFocus()
		m.renderVP()
		return true
	}
	if len(m.toolRows) > 0 && !m.toolPanel.Focused {
		return m.focusLiveToolStrip(reverse)
	}
	if len(ids) == 0 {
		m.selectedBlockID = ""
		m.setFocus(focusComposer)
		m.renderVP() // repaint without selection chrome
		return true
	}
	idx := -1
	for i, id := range ids {
		if id == m.selectedBlockID {
			idx = i
			break
		}
	}
	if !reverse {
		if idx < 0 {
			m.selectedBlockID = ids[0]
		} else if idx >= len(ids)-1 {
			m.selectedBlockID = ""
			m.leaveToolPanelFocus()
			m.setFocus(focusComposer)
			m.renderVP() // clear chrome when wrapping to composer
			return true
		} else {
			m.selectedBlockID = ids[idx+1]
		}
	} else {
		if idx < 0 {
			m.selectedBlockID = ids[len(ids)-1]
		} else if idx == 0 {
			m.selectedBlockID = ""
			m.leaveToolPanelFocus()
			m.setFocus(focusComposer)
			m.renderVP()
			return true
		} else {
			m.selectedBlockID = ids[idx-1]
		}
	}
	m.ensureSelectedVisible()
	m.renderVP()
	return true
}

func (m *tuiModel) focusLiveToolStrip(reverse bool) bool {
	if len(m.toolRows) == 0 {
		return false
	}
	m.toolPanel.Focused = true
	m.toolPanel.ordered = orderToolIndices(m.toolRows)
	if reverse {
		m.toolPanel.Selected = m.toolPanel.ordered[len(m.toolPanel.ordered)-1]
	} else {
		m.toolPanel.Selected = m.toolPanel.ordered[0]
	}
	m.renderVP()
	return true
}

func (m *tuiModel) leaveToolPanelFocus() {
	m.toolPanel.Focused = false
	m.toolPanel.Selected = -1
	m.selectedBlockID = ""
	m.setFocus(focusComposer)
}

// ensureSelectedVisible scrolls the viewport so the selected block range is on screen.
func (m *tuiModel) ensureSelectedVisible() {
	if m.selectedBlockID == "" {
		return
	}
	r, ok := m.chatBlockRanges[m.selectedBlockID]
	if !ok || r[1] <= r[0] {
		return
	}
	m.followOutput = false
	top := m.viewport.YOffset
	h := m.viewport.Height
	if h <= 0 {
		h = 1
	}
	bottom := top + h
	if r[0] < top {
		m.viewport.YOffset = r[0]
		return
	}
	if r[1] > bottom {
		m.viewport.YOffset = max(0, r[1]-h)
	}
}

// applySelectionChrome dim-highlights the selected block's lines without changing
// line count (hit map Y stays valid). Blank lanes are left alone.
func applySelectionChrome(lines []string, ranges map[string][2]int, selectedID string, focused bool) []string {
	if !focused || selectedID == "" || len(lines) == 0 {
		return lines
	}
	r, ok := ranges[selectedID]
	if !ok || r[1] <= r[0] {
		return lines
	}
	start, end := r[0], r[1]
	if start < 0 {
		start = 0
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start >= end {
		return lines
	}
	out := make([]string, len(lines))
	copy(out, lines)
	// Subtle bar — borderless selection (color 237 dark gray).
	sel := lipgloss.NewStyle().Background(lipgloss.Color("237"))
	for i := start; i < end; i++ {
		plain := stripANSI(out[i])
		if strings.TrimSpace(plain) == "" {
			continue
		}
		// Preserve width: pad plain to original visible width then paint bg.
		vis := visibleWidth(out[i])
		if vis < 1 {
			vis = len([]rune(plain))
		}
		row := plain
		if visibleWidth(row) < vis {
			row += strings.Repeat(" ", vis-visibleWidth(row))
		}
		out[i] = sel.Render(row)
	}
	return out
}

// clearStaleSelection drops selectedBlockID if it is no longer in ranges.
func (m *tuiModel) clearStaleSelection() {
	if m.selectedBlockID == "" {
		return
	}
	if _, ok := m.chatBlockRanges[m.selectedBlockID]; !ok {
		m.selectedBlockID = ""
	}
}
