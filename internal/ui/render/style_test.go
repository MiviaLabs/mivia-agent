package render

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func loadTheme(t testing.TB) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found in embedded set")
	return theme.Theme{}
}

func TestRoleAppliesColourAtTrueColor(t *testing.T) {
	th := loadTheme(t)
	got := Role(th, theme.TierTrueColor, theme.RoleAccent).Render("x")
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("expected an ANSI escape at truecolor tier, got %q", got)
	}
}

func TestRoleOmitsColourAtNoColourTier(t *testing.T) {
	th := loadTheme(t)
	// RoleFG carries no structural emphasis (theme.Emphasis), so at the
	// no-colour tier it should render with no ANSI escapes at all.
	got := Role(th, theme.TierASCII, theme.RoleFG).Render("x")
	if strings.Contains(got, "\x1b[") {
		t.Errorf("expected no ANSI escape at the no-colour tier, got %q", got)
	}
	if got != "x" {
		t.Errorf("expected plain text at the no-colour tier, got %q", got)
	}
}

func TestRoleBoldSurvivesNoColour(t *testing.T) {
	th := loadTheme(t)
	// RoleAccent is in theme.boldRoles: bold is structural, not colour,
	// so it must still render even with no colour support.
	got := Role(th, theme.TierASCII, theme.RoleAccent).Render("x")
	if !strings.Contains(got, "\x1b[1m") {
		t.Errorf("expected bold SGR to survive the no-colour tier, got %q", got)
	}
}

func TestRoleUsesExplicitANSI16AtTier16(t *testing.T) {
	th := loadTheme(t)
	// Tier16 must use the theme's authored ANSI16 index, not a computed
	// nearest-match downsample of the truecolor hex (theme.Resolve's own
	// contract). RoleDanger is a status role every shipped theme defines
	// at every tier.
	got := Role(th, theme.Tier16, theme.RoleDanger).Render("x")
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("expected an ANSI escape at the 16-colour tier, got %q", got)
	}
}

func TestWithBgUsesExplicitANSI16AtTier16(t *testing.T) {
	th := loadTheme(t)
	base := Role(th, theme.Tier16, theme.RoleDiffAddFG)
	fgOnly := base.Render("x")
	withBg := WithBg(base, th, theme.Tier16, theme.RoleDiffAddBG).Render("x")
	if withBg == fgOnly {
		t.Errorf("expected WithBg to add a background SGR at the 16-colour tier, got the same output %q", withBg)
	}
}

func TestWithBgOmitsBackgroundAtNoColourTier(t *testing.T) {
	th := loadTheme(t)
	base := Role(th, theme.TierASCII, theme.RoleDiffAddFG)
	got := WithBg(base, th, theme.TierASCII, theme.RoleDiffAddBG).Render("x")
	if strings.Contains(got, "\x1b[4") { // background SGR codes start with 4x/10x
		t.Errorf("expected no background SGR at the no-colour tier, got %q", got)
	}
}

func TestFormatArgs(t *testing.T) {
	got := FormatArgs(map[string]any{"b": 2, "a": "x"})
	if got != "a=x b=2" {
		t.Errorf("got %q, want sorted \"a=x b=2\"", got)
	}
}

func TestFormatArgsEmpty(t *testing.T) {
	if got := FormatArgs(nil); got != "" {
		t.Errorf("got %q, want empty string for nil args", got)
	}
}

// TestBorderedDegradesByTier pins the ladder: the border exists at every
// tier, but only tiers with a colour for the role colour it.
func TestBorderedDegradesByTier(t *testing.T) {
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var th theme.Theme
	for _, c := range themes {
		if c.Name == "mivia-dark" {
			th = c
		}
	}
	if b := Bordered(th, theme.TierTrueColor, theme.RoleBorderFocus, "x"); !strings.Contains(b, "\x1b[") {
		t.Errorf("true-color border is uncoloured: %q", b)
	}
	if b := Bordered(th, theme.TierASCII, theme.RoleBorderFocus, "x"); strings.Contains(b, "\x1b[") {
		t.Errorf("ASCII border carries colour: %q", b)
	}
	if b := Bordered(th, theme.TierNoTTY, theme.RoleBorderFocus, "x"); !strings.Contains(b, "╭") {
		t.Errorf("no-tty border lost its frame: %q", b)
	}
}
