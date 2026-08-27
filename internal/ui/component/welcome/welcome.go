// Package welcome renders the start screen splash banner for a clean CLI session.
package welcome

import (
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// hintLine is the fixed hint row shown under the identity/workspace lines
// on every tier: no gradient, no animation, one line of static text.
const hintLine = "type a prompt, or / for commands   ·   ctrl+b sidebar"

// Model is the start screen welcome banner component.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier
	frame int

	workspaceRepo   string
	workspaceBranch string
	workspaceOK     bool
}

// New returns an initialized welcome Model. It detects the current
// workspace (repo name and branch) once via no-exec .git inspection so
// the banner can show a workspace line without shelling out to git.
func New(t theme.Theme, tier theme.Tier) Model {
	m := Model{Theme: t, Tier: tier}
	if cwd, err := os.Getwd(); err == nil {
		m.workspaceRepo, m.workspaceBranch, m.workspaceOK = detectWorkspace(cwd)
	}
	return m
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

// identityLine returns the minimal static identity row: an accent-role
// glyph plus "mivia". ASCII/no-tty tiers use a plain "<>" mark; both tiers
// render as a single flat color, with no gradient and no animation.
func (m Model) identityLine() string {
	accent := render.Role(m.Theme, m.Tier, theme.RoleAccent)
	mark := "⬖"
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		mark = "<>"
	}
	return accent.Render(mark + " mivia")
}

// workspaceLine composes the "repoName · branch" text in RoleFGSubtle.
// It returns an empty string when repo is empty, so callers can omit the
// workspace row entirely (not render a blank line) when no repo was
// detected.
func (m Model) workspaceLine(repo, branch string) string {
	if repo == "" {
		return ""
	}
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	return subtle.Render(repo + " · " + branch)
}

func (m Model) bannerLines() []string {
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)

	lines := []string{m.identityLine(), ""}
	if m.workspaceOK {
		if line := m.workspaceLine(m.workspaceRepo, m.workspaceBranch); line != "" {
			lines = append(lines, line, "")
		}
	}
	lines = append(lines, subtle.Render(hintLine))
	return lines
}

// compactLines stays Zen-A-only: identity line plus hint line, never a
// workspace line, matching the compact variant's existing minimalism.
func (m Model) compactLines() []string {
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	return []string{
		m.identityLine(),
		subtle.Render(hintLine),
	}
}

// Rows returns exactly height rows of vertically and horizontally centered welcome content.
func (m Model) Rows(width, height int) []string {
	if height <= 0 || width <= 0 {
		return nil
	}

	var content []string
	if height < 12 || width < 54 {
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
