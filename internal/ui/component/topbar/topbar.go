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
// lives in the status line, which owns the turn) plus the session
// values ports.Conversation reports.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	mark  mark.Model
	info  ports.ModelInfo
	usage ports.Usage
	width int
}

// New returns a top bar showing the given session values. width is the
// terminal width; the bar truncates to it on narrow terminals.
func New(t theme.Theme, tier theme.Tier, info ports.ModelInfo, usage ports.Usage, width int) Model {
	return Model{Theme: t, Tier: tier, mark: mark.New(t, tier, mark.Idle), info: info, usage: usage, width: width}
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

// Height is the row count View draws: always one. A fixed row is the
// whole point - the cockpit reserves it whether or not it has content.
func (m Model) Height() int { return 1 }

// contextPercent is the share of the context window in use, from the
// cumulative in+out token counts. ok is false when the window size is
// unknown: dividing by an unstated window would print a made-up number.
func (m Model) contextPercent() (int, bool) {
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

// View renders the one row. The right side states the model, provider,
// and context share; an unknown window omits the share rather than
// guessing.
func (m Model) View() string {
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)

	left := m.mark.View() + subtle.Render("  ") + fg.Render(Wordmark)
	right := subtle.Render(m.info.Provider+"/") + fg.Render(m.info.Name)
	if pct, ok := m.contextPercent(); ok {
		right += subtle.Render(fmt.Sprintf("  %d%% ctx", pct))
	}
	var line string
	if m.width > 0 {
		gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
		if gap < 1 {
			gap = 1
		}
		line = left + strings.Repeat(" ", gap) + right
		return ansi.Truncate(line, m.width, "")
	}
	return left + " " + right
}
