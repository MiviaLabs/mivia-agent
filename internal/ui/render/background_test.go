package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// TestFillBGReopensAfterEveryReset is the primitive's whole point: a
// lipgloss Background() colours a block only up to its first inner
// reset, and styled text is a chain of runs that each end in one.
func TestFillBGReopensAfterEveryReset(t *testing.T) {
	th := dialogTheme(t)
	tier := theme.TierTrueColor
	text := Role(th, tier, theme.RoleKeyword).Render("func") + Role(th, tier, theme.RoleFG).Render(" x()")

	got := FillBG(th, tier, theme.RoleBG, text)
	if n := unpaintedCells(got); n > 0 {
		t.Errorf("%d cell(s) left unpainted: %q", n, got)
	}
	if !strings.HasSuffix(got, "\x1b[m") {
		t.Errorf("fill does not close with a reset, so it bleeds past its content: %q", got)
	}
	if ansi.Strip(got) != ansi.Strip(text) {
		t.Errorf("fill changed the visible text: %q vs %q", ansi.Strip(got), ansi.Strip(text))
	}
}

// TestFillBGPaintsEveryLine: a multi-line block is filled row by row, so
// a row that ends short does not leave the next one unpainted.
func TestFillBGPaintsEveryLine(t *testing.T) {
	th := dialogTheme(t)
	got := FillBG(th, theme.TierTrueColor, theme.RoleBG, "one\ntwo\nthree")
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines, want 3", len(lines))
	}
	for i, ln := range lines {
		if n := unpaintedCells(ln); n > 0 {
			t.Errorf("line %d has %d unpainted cell(s): %q", i, n, ln)
		}
	}
}

// TestFillBGKeepsARunsOwnBackground: a run that already carries a
// background (a diff line, a selected picker row) must keep it, and the
// fill must pick up again after it.
func TestFillBGKeepsARunsOwnBackground(t *testing.T) {
	th := dialogTheme(t)
	tier := theme.TierTrueColor
	own := WithBg(Role(th, tier, theme.RoleDiffAddFG), th, tier, theme.RoleDiffAddBG).Render("+ added")
	got := FillBG(th, tier, theme.RoleBG, own+Role(th, tier, theme.RoleFG).Render(" tail"))

	// The parameters, not the whole sequence: lipgloss fuses a run's
	// foreground and background into one SGR.
	addBG := strings.TrimSuffix(strings.TrimPrefix(bgSeq(th.Resolve(theme.RoleDiffAddBG, tier)), "\x1b["), "m")
	if addBG == "" {
		t.Fatal("mivia-dark defines no diff-add background; this test needs one")
	}
	if !strings.Contains(got, addBG) {
		t.Errorf("the run's own background was overwritten: %q", got)
	}
	if n := unpaintedCells(got); n > 0 {
		t.Errorf("%d cell(s) left unpainted after the run's own background: %q", n, got)
	}
}

// TestFillBGIsANoOpWithoutColour holds the degradation ladder: a colour
// fill without colour is nothing, so NO_COLOR output stays byte-identical.
func TestFillBGIsANoOpWithoutColour(t *testing.T) {
	th := dialogTheme(t)
	for _, tier := range []theme.Tier{theme.TierASCII, theme.TierNoTTY} {
		const text = "plain row"
		if got := FillBG(th, tier, theme.RoleBG, text); got != text {
			t.Errorf("tier %v changed the output: %q", tier, got)
		}
	}
}

// TestFillBGAt16ColorsUsesTheAuthoredIndex covers the ANSI16 arm: the
// theme's authored 16-colour index, not a skipped fill.
func TestFillBGAt16ColorsUsesTheAuthoredIndex(t *testing.T) {
	th := dialogTheme(t)
	got := FillBG(th, theme.Tier16, theme.RoleBG, "row")
	if got == "row" {
		t.Errorf("16-colour tier painted nothing: %q", got)
	}
	if n := unpaintedCells(got); n > 0 {
		t.Errorf("%d cell(s) left unpainted at the 16-colour tier: %q", n, got)
	}
}
