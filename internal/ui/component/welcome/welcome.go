// Package welcome renders the start screen splash banner for a clean CLI session.
package welcome

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// Model is the start screen welcome banner component.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier
	frame int
}

// New returns an initialized welcome Model.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier}
}

// SetTheme updates the theme and tier of the welcome component.
func (m *Model) SetTheme(t theme.Theme, tier theme.Tier) {
	m.Theme, m.Tier = t, tier
}

// UpdateFrame advances the optional animation frame.
func (m Model) UpdateFrame() Model {
	m.frame++
	return m
}

func (m Model) logoLines() []string {
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		return []string{
			`   /\   `,
			`  /  \  `,
			` / /\ \ `,
			` \ \/ / `,
			`  \  /  `,
			`   \/   `,
		}
	}
	return []string{
		"   ▲   ",
		" ◢███◣ ",
		"◥█████◤",
		" ◥███◤ ",
		"   ▼   ",
	}
}

func (m Model) renderLegend() string {
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		return render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render("states: < idle   ^ thinking   > running   . writing")
	}
	idle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render("⬖ idle")
	thinking := render.Role(m.Theme, m.Tier, theme.RoleAccent).Render("⬘ thinking")
	running := render.Role(m.Theme, m.Tier, theme.RoleInfo).Render("◈ running")
	streaming := render.Role(m.Theme, m.Tier, theme.RoleFG).Render("◇ streaming")
	sep := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render("   ")
	return idle + sep + thinking + sep + running + sep + streaming
}

func (m Model) bannerLines() []string {
	var lines []string
	accent := render.Role(m.Theme, m.Tier, theme.RoleAccent)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)

	for _, l := range m.logoLines() {
		lines = append(lines, accent.Render(l))
	}
	lines = append(lines, "")
	lines = append(lines, accent.Render("M I V I A   A G E N T"))
	lines = append(lines, fg.Render("Autonomous Coding Assistant"))
	lines = append(lines, "")
	lines = append(lines, subtle.Render("Author: Mac Lisowski  •  MIT License"))
	lines = append(lines, m.renderLegend())
	lines = append(lines, "")
	lines = append(lines, subtle.Render("type a prompt or / for commands  •  tab:sidebar  •  ctrl+c:quit"))
	return lines
}

func (m Model) compactLines() []string {
	accent := render.Role(m.Theme, m.Tier, theme.RoleAccent)
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	markGlyph := "⬖ "
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		markGlyph = "< "
	}
	return []string{
		accent.Render(markGlyph+"Mivia Agent") + subtle.Render(" — Autonomous Coding Assistant"),
		subtle.Render("Author: Mac Lisowski  •  MIT License"),
		subtle.Render("type a prompt or / for commands  •  tab:sidebar  •  ctrl+c:quit"),
	}
}

// Rows returns exactly height rows of vertically and horizontally centered welcome content.
func (m Model) Rows(width, height int) []string {
	if height <= 0 || width <= 0 {
		return nil
	}

	var content []string
	if height < 12 || width < 45 {
		content = m.compactLines()
	} else {
		content = m.bannerLines()
	}

	centeredContent := make([]string, len(content))
	for i, line := range content {
		w := ansi.StringWidth(line)
		if w >= width {
			centeredContent[i] = ansi.Truncate(line, width, "")
			continue
		}
		padLeft := (width - w) / 2
		centeredContent[i] = strings.Repeat(" ", padLeft) + line
	}

	topPad := max(0, (height-len(centeredContent))/2)
	var out []string
	for i := 0; i < topPad && len(out) < height; i++ {
		out = append(out, "")
	}
	for _, line := range centeredContent {
		if len(out) < height {
			out = append(out, line)
		}
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

// View returns the joined rendered rows.
func (m Model) View(width, height int) string {
	return strings.Join(m.Rows(width, height), "\n")
}
