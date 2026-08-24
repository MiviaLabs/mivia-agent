// Package topbar is the cockpit's fixed top row: the brand mark and
// wordmark on the left, the session's model and context usage on the
// right. It is session identity - it changes at turn boundaries, never
// per token - so the row is drawn once per update and never reflows
// anything (it is a fixed reserved row, like the status row).
package topbar

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/mark"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// Wordmark is the product wordmark. Lowercase: the binary is mivia and
// the product name in AGENTS.md is mivia; the mock's title-case
// "Mivia" is prose, not the wordmark.
const Wordmark = "mivia"

// Model is the top bar's state: an idle mark (the animated instance
// lives in the status line, which owns the turn), the session
// values ports.Conversation reports, and an optional breadcrumb trail.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	mark        mark.Model
	info        ports.ModelInfo
	usage       ports.Usage
	breadcrumb  []string
	width       int
	filesCount  int
	agentsCount int
}

// New returns a top bar showing the given session values. width is the
// terminal width; the bar truncates to it on narrow terminals.
func New(t theme.Theme, tier theme.Tier, info ports.ModelInfo, usage ports.Usage, width int) Model {
	return Model{Theme: t, Tier: tier, mark: mark.New(t, tier, mark.Idle), info: info, usage: usage, width: width}
}

// SetActivity updates the live count of touched files and running/dispatched agents.
func (m *Model) SetActivity(filesCount, agentsCount int) {
	m.filesCount = filesCount
	m.agentsCount = agentsCount
}

// SetWidth records a resize.
func (m *Model) SetWidth(w int) { m.width = w }

// SetTheme records a theme change, re-resolving the mark's colours.
func (m *Model) SetTheme(t theme.Theme, tier theme.Tier) {
	m.Theme, m.Tier = t, tier
	m.mark.Theme, m.mark.Tier = t, tier
}

// SetSession updates the model and usage values, e.g. after a turn
// ends and the counts moved.
func (m *Model) SetSession(info ports.ModelInfo, usage ports.Usage) {
	m.info, m.usage = info, usage
}

// SetBreadcrumb records the ordered breadcrumb segments (e.g. [sessionTitle]
// or [sessionTitle, agentName, taskDesc]).
func (m *Model) SetBreadcrumb(segments []string) {
	if len(segments) == 0 {
		m.breadcrumb = nil
		return
	}
	m.breadcrumb = make([]string, len(segments))
	copy(m.breadcrumb, segments)
}

// Height is the row count View draws: 2 when a breadcrumb is present,
// 1 otherwise.
func (m Model) Height() int {
	if len(m.breadcrumb) > 0 {
		return 2
	}
	return 1
}

// ContextPercent is the share of the context window in use, from the
// cumulative in+out token counts. ok is false when the window size is
// unknown: dividing by an unstated window would print a made-up
// number. Exported so the status line (a different component) can
// show the same share the top bar already computes, rather than
// re-deriving it from a second copy of ModelInfo/Usage.
func (m Model) ContextPercent() (int, bool) {
	if m.info.ContextWindow <= 0 {
		return 0, false
	}
	used := m.usage.InputTokens + m.usage.OutputTokens
	pct := int(100 * used / m.info.ContextWindow)
	if pct > 999 {
		pct = 999
	}
	return pct, true
}

func (m Model) breadcrumbRow() string {
	if len(m.breadcrumb) == 0 {
		return ""
	}
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)

	sep := " › "
	ellipsis := "… "
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		sep = " > "
		ellipsis = "... "
	}

	buildTrail := func(startIdx int) string {
		if startIdx >= len(m.breadcrumb) {
			return ""
		}
		var b strings.Builder
		if startIdx > 0 {
			b.WriteString(subtle.Render(ellipsis))
		}
		for i := startIdx; i < len(m.breadcrumb); i++ {
			if i > startIdx {
				b.WriteString(subtle.Render(sep))
			}
			if i == len(m.breadcrumb)-1 {
				b.WriteString(fg.Render(m.breadcrumb[i]))
			} else {
				b.WriteString(subtle.Render(m.breadcrumb[i]))
			}
		}
		return b.String()
	}

	row := buildTrail(0)
	if m.width <= 0 || ansi.StringWidth(row) <= m.width {
		return row
	}

	// Try eliding leading segments one by one so segment boundaries are preserved
	for start := 1; start < len(m.breadcrumb); start++ {
		candidate := buildTrail(start)
		if ansi.StringWidth(candidate) <= m.width {
			return candidate
		}
	}

	// If even the last segment with ellipsis doesn't fit, truncate from the left
	prefix := subtle.Render(ellipsis)
	if m.width >= ansi.StringWidth(prefix) {
		row = ansi.TruncateLeft(row, m.width, prefix)
	} else {
		row = ansi.TruncateLeft(row, m.width, "")
	}
	if ansi.StringWidth(row) > m.width {
		row = ansi.Truncate(row, m.width, "")
	}
	return row
}

func (m Model) contextBadge(pct int) string {
	border := render.Role(m.Theme, m.Tier, theme.RoleBorder)
	role := theme.RoleFGSubtle
	if pct >= 90 {
		role = theme.RoleDanger
	} else if pct >= 70 {
		role = theme.RoleWarning
	}
	style := render.Role(m.Theme, m.Tier, role)

	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY || m.width < 70 {
		return border.Render("[ ") + style.Render(fmt.Sprintf("%d%%", pct)) + border.Render(" ]")
	}

	totalBlocks := 4
	filled := min(totalBlocks, max(0, (pct*totalBlocks+50)/100))
	bar := strings.Repeat("▰", filled) + strings.Repeat("▱", totalBlocks-filled)

	return border.Render("[ ") + style.Render(fmt.Sprintf("%d%% ", pct)+bar) + border.Render(" ]")
}

func (m Model) modelCapsule() string {
	border := render.Role(m.Theme, m.Tier, theme.RoleBorder)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)

	var label string
	if m.info.Provider != "" {
		label = subtle.Render(m.info.Provider+"/") + fg.Render(m.info.Name)
	} else {
		label = fg.Render(m.info.Name)
	}
	return border.Render("[ ") + label + border.Render(" ]")
}

func (m Model) activityBadge() string {
	if m.filesCount == 0 && m.agentsCount == 0 {
		return ""
	}
	border := render.Role(m.Theme, m.Tier, theme.RoleBorder)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	var parts []string
	if m.filesCount > 0 {
		label := fmt.Sprintf("%d files", m.filesCount)
		if m.filesCount == 1 {
			label = "1 file"
		}
		parts = append(parts, subtle.Render(label))
	}
	if m.agentsCount > 0 {
		label := fmt.Sprintf("⚡ %d agents", m.agentsCount)
		if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
			label = fmt.Sprintf("%d agents", m.agentsCount)
		}
		if m.agentsCount == 1 {
			label = "⚡ 1 agent"
			if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
				label = "1 agent"
			}
		}
		parts = append(parts, render.Role(m.Theme, m.Tier, theme.RoleAccent).Render(label))
	}
	return border.Render("[ ") + strings.Join(parts, subtle.Render(" | ")) + border.Render(" ]")
}

// View renders the top bar. The first row states the mark/wordmark on the
// left, and model/provider/context share on the right. When breadcrumbs
// are present, a second row renders the trail.
func (m Model) View() string {
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)

	left := m.mark.View() + subtle.Render("  ") + fg.Render(Wordmark)
	if act := m.activityBadge(); act != "" && (m.width <= 0 || m.width >= 70) {
		left += " " + act
	}
	right := m.modelCapsule()
	if pct, ok := m.ContextPercent(); ok {
		right += " " + m.contextBadge(pct)
	}
	var line string
	if m.width > 0 {
		gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
		if gap < 1 {
			gap = 1
		}
		line = left + strings.Repeat(" ", gap) + right
		line = ansi.Truncate(line, m.width, "")
	} else {
		line = left + " " + right
	}

	if len(m.breadcrumb) > 0 {
		return line + "\n" + m.breadcrumbRow()
	}
	return line
}
