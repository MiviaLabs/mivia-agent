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

// ascii reports the tiers that cannot show the glyph or box-drawing marks.
func (m Model) ascii() bool {
	return m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY
}

// mark is the brand glyph: "⬖", or "<>" where the glyph cannot be shown.
func (m Model) mark() string {
	if m.ascii() {
		return "<>"
	}
	return "⬖"
}

// identityLine returns the compact identity row: an accent-role glyph plus
// "mivia", as a single flat colour, no gradient and no animation.
func (m Model) identityLine() string {
	accent := render.Role(m.Theme, m.Tier, theme.RoleAccent)
	return accent.Render(m.mark() + " mivia")
}

// brandLine is the full banner's identity row: the glyph in the warning
// role - the product's one accent, worn here as a fixed brand mark rather
// than a status - and the wordmark letter-spaced in the foreground role.
func (m Model) brandLine() string {
	warn := render.Role(m.Theme, m.Tier, theme.RoleWarning)
	fg := render.Role(m.Theme, m.Tier, theme.RoleFG).Bold(true)
	return warn.Render(m.mark()) + "  " + fg.Render("m i v i a")
}

// tagline is the product's one-line thesis, quiet and italic, shared
// verbatim with the web home page's headline.
const tagline = "for the work that takes longer than a chat."

// boxPad is the columns of padding between the box's border and its
// widest line, and the rows of padding above and below its content.
const boxPad = 4

// boxed frames lines in a single-rule, square-cornered box (no rounded
// corners: the frame is a panel, not a dialog) sized to the widest line
// plus boxPad each side, every line centred inside it. ASCII tiers get
// "+-|". Lines are returned unframed when the box would not fit width.
func (m Model) boxed(lines []string, width int) []string {
	inner := 0
	for _, ln := range lines {
		inner = max(inner, ansi.StringWidth(ln))
	}
	inner += 2 * boxPad
	if inner+2 > width {
		return lines
	}
	tl, tr, bl, br, h, v := "┌", "┐", "└", "┘", "─", "│"
	if m.ascii() {
		tl, tr, bl, br, h, v = "+", "+", "+", "+", "-", "|"
	}
	border := render.Role(m.Theme, m.Tier, theme.RoleBorder)
	side := border.Render(v)
	blank := side + strings.Repeat(" ", inner) + side
	out := make([]string, 0, len(lines)+4)
	out = append(out, border.Render(tl+strings.Repeat(h, inner)+tr), blank)
	for _, ln := range lines {
		w := ansi.StringWidth(ln)
		left := (inner - w) / 2
		out = append(out, side+strings.Repeat(" ", left)+ln+strings.Repeat(" ", inner-w-left)+side)
	}
	out = append(out, blank, border.Render(bl+strings.Repeat(h, inner)+br))
	return out
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

// bannerLines is the full banner: a square-cornered panel holding the
// brand line, the workspace line (when a repo was detected), a short
// divider, the tagline, and the hint line, with a blank row between the
// groups. No label sits on the frame: the panel is content, not a
// dialog.
func (m Model) bannerLines(width int) []string {
	subtle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	muted := render.Role(m.Theme, m.Tier, theme.RoleFGMuted).Italic(true)
	border := render.Role(m.Theme, m.Tier, theme.RoleBorder)

	lines := []string{m.brandLine()}
	if m.workspaceOK {
		if line := m.workspaceLine(m.workspaceRepo, m.workspaceBranch); line != "" {
			lines = append(lines, line)
		}
	}
	lines = append(lines,
		"",
		border.Render("·  ·  ·"),
		"",
		muted.Render(tagline),
		"",
		subtle.Render(hintLine),
	)
	return m.boxed(lines, width)
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
	if height < 16 || width < 54 {
		content = m.compactLines()
	} else {
		content = m.bannerLines(width)
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
