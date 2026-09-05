package topbar

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// SessionTab describes one session for top bar tab rendering and hit testing.
type SessionTab struct {
	ID          string
	Title       string
	Index       int  // 1-based display index (e.g. 1 for "1:title")
	IsCurrent   bool // true if this is the active foreground session
	Running     bool // true if turn is in progress
	NeedsAction bool // true if waiting for user tool approval
}

// TabBound defines the horizontal click bounds for a rendered tab.
type TabBound struct {
	Tab      SessionTab
	StartCol int
	EndCol   int
}

// SetTabs updates the topbar's session tabs.
func (m *Model) SetTabs(tabs []SessionTab) {
	if len(tabs) == 0 {
		m.tabs = nil
		return
	}
	m.tabs = make([]SessionTab, len(tabs))
	copy(m.tabs, tabs)
}

// Tabs returns the current session tabs.
func (m Model) Tabs() []SessionTab {
	return m.tabs
}

func (m Model) overflowLeftMarker() string {
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		return "< "
	}
	return "◂ "
}

func (m Model) overflowRightMarker(rem int) string {
	if rem <= 0 {
		return ""
	}
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		return fmt.Sprintf(" > (+%d)", rem)
	}
	return fmt.Sprintf(" ▸ (+%d)", rem)
}

func (m Model) renderTab(t SessionTab, maxTitleLen int) string {
	border := render.Role(m.Theme, m.Tier, theme.RoleBorder)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)

	var markStr string
	switch {
	case t.NeedsAction:
		warn := render.Role(m.Theme, m.Tier, theme.RoleWarning)
		markStr = warn.Render("!")
	case t.Running:
		acc := render.Role(m.Theme, m.Tier, theme.RoleAccent)
		glyph := "⚡"
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			glyph = "~"
		}
		markStr = acc.Render(glyph)
	case t.IsCurrent:
		glyph := "●"
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			glyph = ">"
		}
		markStr = fg.Render(glyph)
	default:
		glyph := "○"
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			glyph = "-"
		}
		markStr = subtle.Render(glyph)
	}

	title := t.Title
	if title == "" {
		title = t.ID
	}
	title = ansi.Strip(title)
	title = strings.ReplaceAll(title, "\r", " ")
	title = strings.ReplaceAll(title, "\n", " ")

	if maxTitleLen > 0 && ansi.StringWidth(title) > maxTitleLen {
		ellipsis := "…"
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			ellipsis = "..."
		}
		title = ansi.Truncate(title, maxTitleLen, ellipsis)
	}

	label := fmt.Sprintf("%d:%s", t.Index, title)
	if t.IsCurrent {
		return border.Render("[ ") + markStr + " " + fg.Bold(true).Render(label) + border.Render(" ]")
	}
	return border.Render(" ") + markStr + " " + subtle.Render(label) + border.Render(" ")
}

func (m Model) computeTabWindow(availWidth int, startColOffset int) (startIdx, endIdx int, renderedTabs []string, tabBounds []TabBound, startCol int) {
	if len(m.tabs) == 0 || availWidth <= 0 {
		return 0, 0, nil, nil, startColOffset
	}

	currIdx := 0
	for i, t := range m.tabs {
		if t.IsCurrent {
			currIdx = i
			break
		}
	}

	standardRendered := make([]string, len(m.tabs))
	widths := make([]int, len(m.tabs))
	totalWidth := 0
	for i, t := range m.tabs {
		standardRendered[i] = m.renderTab(t, 16)
		widths[i] = ansi.StringWidth(standardRendered[i])
		totalWidth += widths[i]
		if i > 0 {
			totalWidth++ // 1 space separator
		}
	}

	if totalWidth <= availWidth {
		bounds := make([]TabBound, len(m.tabs))
		col := startColOffset
		for i, t := range m.tabs {
			w := widths[i]
			bounds[i] = TabBound{
				Tab:      t,
				StartCol: col,
				EndCol:   col + w,
			}
			col += w + 1
		}
		return 0, len(m.tabs), standardRendered, bounds, startColOffset
	}

	initialOverhead := 0
	if currIdx > 0 {
		initialOverhead += ansi.StringWidth(m.overflowLeftMarker())
	}
	if currIdx < len(m.tabs)-1 {
		initialOverhead += ansi.StringWidth(m.overflowRightMarker(len(m.tabs) - 1 - currIdx))
	}
	if widths[currIdx]+initialOverhead > availWidth {
		return 0, 0, nil, nil, startColOffset
	}

	// Sliding window around currIdx
	start, end := m.slideWindowRange(currIdx, widths, availWidth)

	var finalTabs []string
	var bounds []TabBound
	col := startColOffset

	if start > 0 {
		col += ansi.StringWidth(m.overflowLeftMarker())
	}

	for i := start; i < end; i++ {
		t := m.tabs[i]
		str := standardRendered[i]
		w := ansi.StringWidth(str)
		finalTabs = append(finalTabs, str)
		bounds = append(bounds, TabBound{
			Tab:      t,
			StartCol: col,
			EndCol:   col + w,
		})
		col += w + 1
	}

	return start, end, finalTabs, bounds, startColOffset
}

func (m Model) slideWindowRange(currIdx int, widths []int, availWidth int) (start, end int) {
	start = currIdx
	end = currIdx + 1
	used := widths[currIdx]

	for {
		expanded := false
		if end < len(m.tabs) {
			extra := widths[end] + 1
			rightOverhead := 0
			if end+1 < len(m.tabs) {
				rem := len(m.tabs) - (end + 1)
				rightOverhead = ansi.StringWidth(m.overflowRightMarker(rem))
			}
			leftOverhead := 0
			if start > 0 {
				leftOverhead = ansi.StringWidth(m.overflowLeftMarker())
			}
			if used+extra+leftOverhead+rightOverhead <= availWidth {
				used += extra
				end++
				expanded = true
			}
		}
		if start > 0 {
			extra := widths[start-1] + 1
			leftOverhead := 0
			if start-1 > 0 {
				leftOverhead = ansi.StringWidth(m.overflowLeftMarker())
			}
			rightOverhead := 0
			if end < len(m.tabs) {
				rem := len(m.tabs) - end
				rightOverhead = ansi.StringWidth(m.overflowRightMarker(rem))
			}
			if used+extra+leftOverhead+rightOverhead <= availWidth {
				used += extra
				start--
				expanded = true
			}
		}
		if !expanded {
			break
		}
	}
	return start, end
}

// TabBounds returns the 0-indexed column range [startCol, endCol) of the
// session tab at index within the content width. Returns ok = false if not visible.
func (m Model) TabBounds(index int) (startCol, endCol int, ok bool) {
	if index < 0 || index >= len(m.tabs) {
		return 0, 0, false
	}
	plan := m.planLayout()
	_, _, _, bounds, _ := m.computeTabWindow(plan.availTabs, plan.startCol)
	targetID := m.tabs[index].ID
	for _, b := range bounds {
		if b.Tab.ID == targetID {
			return b.StartCol, b.EndCol, true
		}
	}
	return 0, 0, false
}

// HitTab reports which tab falls under clickCol.
func (m Model) HitTab(clickCol int) (tab SessionTab, ok bool) {
	if len(m.tabs) == 0 {
		return SessionTab{}, false
	}
	plan := m.planLayout()
	_, _, _, bounds, _ := m.computeTabWindow(plan.availTabs, plan.startCol)
	for _, b := range bounds {
		if clickCol >= b.StartCol && clickCol < b.EndCol {
			return b.Tab, true
		}
	}
	return SessionTab{}, false
}

func (m Model) renderTabStrip(availWidth int, startCol int) string {
	start, end, renderedTabs, _, _ := m.computeTabWindow(availWidth, startCol)
	if len(renderedTabs) == 0 {
		return ""
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString(render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(m.overflowLeftMarker()))
	}
	for i, r := range renderedTabs {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(r)
	}
	if end < len(m.tabs) {
		rem := len(m.tabs) - end
		b.WriteString(render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(m.overflowRightMarker(rem)))
	}
	return b.String()
}
