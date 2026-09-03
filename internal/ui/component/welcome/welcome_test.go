package welcome

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil || len(themes) == 0 {
		t.Fatalf("failed to load embedded themes: %v", err)
	}
	return themes[0]
}

// containsWord reports whether word appears in s as a whole word (not as
// a substring of a longer word), so legend-removal assertions cannot be
// fooled by an unrelated string that happens to contain the same letters.
func containsWord(s, word string) bool {
	for _, field := range strings.FieldsFunc(s, func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('A' <= r && r <= 'Z')
	}) {
		if field == word {
			return true
		}
	}
	return false
}

func TestWelcomeBannerFullRendering(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)

	rows := m.Rows(80, 20)
	if len(rows) != 20 {
		t.Fatalf("Rows count = %d, want 20", len(rows))
	}

	view := ansi.Strip(m.View(80, 20))
	if !strings.Contains(view, "⬖  m i v i a") {
		t.Errorf("missing brand line '⬖  m i v i a' in view:\n%s", view)
	}
	if strings.Contains(view, "Ｍ Ｉ Ｖ Ｉ Ａ") {
		t.Errorf("fullwidth wordmark must be removed from view:\n%s", view)
	}
	for _, legendWord := range []string{"thinking", "running", "idle", "writing"} {
		if containsWord(view, legendWord) {
			t.Errorf("legend word %q must be removed from view:\n%s", legendWord, view)
		}
	}
	if !strings.Contains(view, "type a prompt, or / for commands   ·   ctrl+b sidebar") {
		t.Errorf("missing new hint line in view:\n%s", view)
	}
	if !strings.Contains(view, "for the work that takes longer than a chat.") {
		t.Errorf("welcome banner should show the tagline:\n%s", view)
	}
	// The panel is a square-cornered box with no label on its frame.
	if !strings.Contains(view, "┌") || !strings.Contains(view, "┘") {
		t.Errorf("welcome banner should sit in a square-cornered box:\n%s", view)
	}
	if strings.Contains(view, "╭") || strings.Contains(view, "╰") {
		t.Errorf("welcome banner box must not use rounded corners:\n%s", view)
	}
	if strings.Contains(view, "home") {
		t.Errorf("welcome banner frame must carry no label:\n%s", view)
	}
	if strings.Contains(view, "Mac Lisowski") {
		t.Errorf("welcome banner should not show the author credit:\n%s", view)
	}
	if strings.Contains(view, "MIT License") {
		t.Errorf("welcome banner should not show the license line:\n%s", view)
	}
}

func TestWelcomeCompactRendering(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)

	rows := m.Rows(80, 6)
	if len(rows) != 6 {
		t.Fatalf("compact Rows count = %d, want 6", len(rows))
	}

	view := ansi.Strip(m.View(80, 6))
	if !strings.Contains(view, "mivia") {
		t.Errorf("compact view missing title:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+b sidebar") {
		t.Errorf("compact view missing keybinding hint:\n%s", view)
	}
	if strings.Contains(view, "Mac Lisowski") {
		t.Errorf("compact view should not show the author credit:\n%s", view)
	}
	if strings.Contains(view, "for the work") {
		t.Errorf("compact view should not show the tagline:\n%s", view)
	}
}

// TestWelcomeCompactNeverShowsWorkspace pins that the compact variant
// never renders a workspace line, regardless of what a real repo detects
// as workspaceOK/workspaceRepo/workspaceBranch on the model.
func TestWelcomeCompactNeverShowsWorkspace(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	// Force workspace detection to a known-populated state directly on the
	// model, bypassing New()'s real .git lookup, so this test does not
	// depend on the sandbox's actual .git state.
	m.workspaceRepo = "mivia-agent"
	m.workspaceBranch = "dev"
	m.workspaceOK = true

	view := ansi.Strip(m.View(80, 6))
	if strings.Contains(view, "mivia-agent · dev") {
		t.Errorf("compact view must never show a workspace line:\n%s", view)
	}
}

func TestWelcomeASCIITierRendering(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierASCII)

	view := ansi.Strip(m.View(80, 20))
	if !strings.Contains(view, "<>  m i v i a") {
		t.Errorf("ASCII tier missing '<>  m i v i a' brand line:\n%s", view)
	}
	if !strings.Contains(view, "+-") || strings.Contains(view, "┌") {
		t.Errorf("ASCII tier must draw the box with +-| only:\n%s", view)
	}
	if strings.Contains(view, "Ｍ Ｉ Ｖ Ｉ Ａ") {
		t.Errorf("ASCII tier must not show fullwidth wordmark glyphs:\n%s", view)
	}
	if !strings.Contains(view, "type a prompt, or / for commands   ·   ctrl+b sidebar") {
		t.Errorf("ASCII tier missing hint line:\n%s", view)
	}
	if strings.Contains(view, "Mac Lisowski") {
		t.Errorf("ASCII tier should not show the author credit:\n%s", view)
	}
	if !strings.Contains(view, "for the work") {
		t.Errorf("ASCII tier should show the tagline:\n%s", view)
	}

	compactView := ansi.Strip(m.View(80, 6))
	if !strings.Contains(compactView, "<> mivia") {
		t.Errorf("ASCII compact view missing '<> mivia' identity line:\n%s", compactView)
	}
}

func TestWelcomeModelMethods(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)

	m.SetTheme(th, theme.TierASCII)
	if m.Tier != theme.TierASCII {
		t.Errorf("SetTheme failed to set tier, got %v, want %v", m.Tier, theme.TierASCII)
	}

	m2 := m.UpdateFrame()
	if m2.frame != 1 {
		t.Errorf("UpdateFrame frame = %d, want 1", m2.frame)
	}

	if rows := m.Rows(0, 0); rows != nil {
		t.Errorf("Rows(0, 0) = %v, want nil", rows)
	}
}

func TestWorkspaceLine(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)

	line := m.workspaceLine("mivia-agent", "dev")
	if !strings.Contains(line, "mivia-agent") || !strings.Contains(line, "dev") {
		t.Errorf("workspaceLine(mivia-agent, dev) = %q, want it to contain both repo and branch", line)
	}
	if !strings.Contains(ansi.Strip(line), "mivia-agent · dev") {
		t.Errorf("workspaceLine(mivia-agent, dev) = %q, want %q joined by ' · '", ansi.Strip(line), "mivia-agent · dev")
	}
}

// TestWorkspaceLineEmptyRepo pins that an empty repo name yields an empty
// string, so bannerLines can omit the row (not render a blank one) when
// no repo was detected.
func TestWorkspaceLineEmptyRepo(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)

	if line := m.workspaceLine("", ""); line != "" {
		t.Errorf("workspaceLine(empty, empty) = %q, want empty string", line)
	}
}

// TestWelcomeBannerOmitsWorkspaceLineWhenNoRepo pins that bannerLines
// composes without a workspace row (and without an extra blank line in
// its place) when workspace detection failed.
func TestWelcomeBannerOmitsWorkspaceLineWhenNoRepo(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.workspaceOK = false
	m.workspaceRepo = ""
	m.workspaceBranch = ""

	lines := m.bannerLines(80)
	for _, line := range lines {
		plain := strings.TrimSpace(ansi.Strip(line))
		if strings.Contains(plain, "·") && !strings.Contains(plain, "ctrl+b") && plain != "│·  ·  ·│" && !strings.HasPrefix(strings.Trim(plain, "│ "), "·  ·  ·") {
			t.Errorf("bannerLines() with no repo must not contain a workspace line, got line %q in %v", line, lines)
		}
	}
}

// TestWelcomeBrandMarkWearsAccentNotWarning: the banner's brand glyph is
// chrome and wears the accent role, like the compact identity line and
// the top bar's mark. RoleWarning is a status ("needs a human") and must
// not be spent on a decoration.
func TestWelcomeBrandMarkWearsAccentNotWarning(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	accent := render.Role(th, theme.TierTrueColor, theme.RoleAccent).Render(m.mark())
	warning := render.Role(th, theme.TierTrueColor, theme.RoleWarning).Render(m.mark())
	if accent == warning {
		t.Skip("theme resolves accent and warning to the same colour; nothing to tell apart")
	}
	line := m.brandLine()
	if !strings.Contains(line, accent) {
		t.Errorf("brand mark must be rendered in the accent role, got %q", line)
	}
	if strings.Contains(line, warning) {
		t.Errorf("brand mark must not wear the warning role, got %q", line)
	}
}
