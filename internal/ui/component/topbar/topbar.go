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
	tabs        []SessionTab
	// sessionHidden drops the model capsule and the context badge from
	// the right side. The activity sidebar shows both in its own
	// sections while it is open, so the bar does not say them twice
	// (SetSessionHidden).
	sessionHidden bool
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

// SetUsage updates the token and cost accounting values incrementally.
func (m *Model) SetUsage(usage ports.Usage) {
	m.usage = usage
}

// Usage returns the current token and cost accounting values.
func (m Model) Usage() ports.Usage {
	return m.usage
}

// Info returns the session's model identity (provider and name).
func (m Model) Info() ports.ModelInfo { return m.info }

// SetSessionHidden hides or shows the model capsule and the context
// badge together. While hidden ModelBounds reports ok = false, so a
// double-click on the bar can no longer open the model picker from
// here - the sidebar's model row owns that while it is open.
func (m *Model) SetSessionHidden(hidden bool) { m.sessionHidden = hidden }

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

// Height is the row count View draws: fixed at 1 row to maximize
// conversation viewport.
func (m Model) Height() int {
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

func (m Model) titleView() string {
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)

	if len(m.breadcrumb) == 0 {
		return fg.Render(Wordmark)
	}

	primary := render.Role(m.Theme, m.Tier, theme.RoleFG).Bold(true).Render(m.breadcrumb[0])
	if len(m.breadcrumb) == 1 {
		return primary
	}

	sep := " › "
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		sep = " > "
	}

	var parts []string
	parts = append(parts, primary)
	for i := 1; i < len(m.breadcrumb); i++ {
		parts = append(parts, subtle.Render(m.breadcrumb[i]))
	}
	return strings.Join(parts, subtle.Render(sep))
}

func (m Model) contextBadge(pct int, withBar bool) string {
	border := render.Role(m.Theme, m.Tier, theme.RoleBorder)
	style := render.Role(m.Theme, m.Tier, render.ContextRole(pct))

	if !withBar || m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY || m.width < 70 {
		return border.Render("[ ") + style.Render(fmt.Sprintf("%d%%", pct)) + border.Render(" ]")
	}

	bar := render.ContextBar(pct, 4, m.Tier)

	return border.Render("[ ") + style.Render(fmt.Sprintf("%d%% ", pct)+bar) + border.Render(" ]")
}

func (m Model) modelCapsule(withProvider bool) string {
	border := render.Role(m.Theme, m.Tier, theme.RoleBorder)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)

	var label string
	if withProvider && m.info.Provider != "" {
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
func (m Model) buildRight(prov, bar bool, pct int, hasPct bool) string {
	if m.sessionHidden {
		return ""
	}
	r := m.modelCapsule(prov)
	if hasPct {
		r += " " + m.contextBadge(pct, bar)
	}
	return r
}

type layoutPlan struct {
	withActivity bool
	withBar      bool
	withProvider bool
	withWordmark bool
	right        string
	left         string
	startCol     int
	availTabs    int
}

func (m Model) planLayout() layoutPlan {
	pct, hasPct := m.ContextPercent()
	p := layoutPlan{
		withActivity: m.width >= 90,
		withBar:      m.width >= 80,
		withProvider: true,
		withWordmark: true,
	}

	calc := func() int {
		p.right = m.buildRight(p.withProvider, p.withBar, pct, hasPct)
		rightW := ansi.StringWidth(p.right)
		avail := m.width - rightW - 1
		p.left, p.startCol = m.buildLeft(p.withActivity, p.withWordmark, avail)
		p.availTabs = avail - p.startCol
		return ansi.StringWidth(p.left) + 1 + rightW
	}

	totalW := calc()
	if m.width > 0 {
		if totalW > m.width && p.withActivity {
			p.withActivity = false
			totalW = calc()
		}
		if totalW > m.width && p.withBar {
			p.withBar = false
			totalW = calc()
		}
		if totalW > m.width && p.withProvider {
			p.withProvider = false
			totalW = calc()
		}
		if totalW > m.width && p.withWordmark && len(m.tabs) > 0 {
			p.withWordmark = false
			_ = calc()
		}
	}
	return p
}

func (m Model) buildLeft(act, wordmark bool, avail int) (string, int) {
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)

	if len(m.tabs) == 0 {
		l := m.mark.View() + subtle.Render("  ") + m.titleView()
		if act {
			if a := m.activityBadge(); a != "" {
				l += " " + a
			}
		}
		return l, 0
	}

	var brand string
	if wordmark {
		brand = m.mark.View() + subtle.Render("  ") + fg.Render(Wordmark)
	} else {
		brand = m.mark.View()
	}
	if act {
		if a := m.activityBadge(); a != "" {
			brand += " " + a
		}
	}
	startCol := ansi.StringWidth(brand) + 1
	availTabs := avail - startCol
	tabsStr := m.renderTabStrip(availTabs, startCol)
	if tabsStr == "" {
		return brand, startCol
	}
	return brand + " " + tabsStr, startCol
}

// View renders the top bar. The first row states the mark/wordmark on the
// left, and model/provider/context share on the right. When breadcrumbs
// are present, a second row renders the trail.
func (m Model) View() string {
	plan := m.planLayout()
	left := plan.left
	right := plan.right

	if m.width > 0 {
		availLeft := m.width - ansi.StringWidth(right) - 1
		if availLeft < ansi.StringWidth(left) {
			if availLeft > 0 {
				left = ansi.Truncate(left, availLeft, "")
			} else {
				left = ""
			}
		}
	}

	var line string
	if m.width > 0 {
		gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
		if gap < 0 {
			gap = 0
		}
		line = left + strings.Repeat(" ", gap) + right
		if ansi.StringWidth(line) > m.width {
			line = ansi.Truncate(line, m.width, "")
		}
	} else {
		line = left + " " + right
	}

	return line
}

// ModelBounds returns the 0-indexed column range [startCol, endCol) of the
// model capsule in the first row of the top bar within the content width.
// Returns ok = false if no model info is displayed.
func (m Model) ModelBounds() (startCol, endCol int, ok bool) {
	if m.info.Name == "" || m.sessionHidden {
		return 0, 0, false
	}
	plan := m.planLayout()
	capsule := m.modelCapsule(plan.withProvider)
	capsuleWidth := ansi.StringWidth(capsule)
	rightWidth := ansi.StringWidth(plan.right)

	startCol = m.width - rightWidth
	if startCol < 0 {
		startCol = 0
	}
	endCol = startCol + capsuleWidth
	return startCol, endCol, true
}

// HitsModel reports whether clickCol falls within the model capsule.
func (m Model) HitsModel(clickCol int) bool {
	startCol, endCol, ok := m.ModelBounds()
	if !ok {
		return false
	}
	return clickCol >= startCol && clickCol < endCol
}

// ActivityBounds returns the 0-indexed column range [startCol, endCol) of the
// activity badge (files/agents) in the top bar within the content width.
// Returns ok = false if no activity badge is displayed.
func (m Model) ActivityBounds() (startCol, endCol int, ok bool) {
	plan := m.planLayout()
	if !plan.withActivity {
		return 0, 0, false
	}
	act := m.activityBadge()
	if act == "" {
		return 0, 0, false
	}

	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)
	brand := m.mark.View() + subtle.Render("  ") + fg.Render(Wordmark)
	brandWidth := ansi.StringWidth(brand)

	startCol = brandWidth + 1
	endCol = startCol + ansi.StringWidth(act)
	return startCol, endCol, true
}

// HitsActivity reports whether clickCol falls within the activity badge.
func (m Model) HitsActivity(clickCol int) bool {
	startCol, endCol, ok := m.ActivityBounds()
	if !ok {
		return false
	}
	return clickCol >= startCol && clickCol < endCol
}
