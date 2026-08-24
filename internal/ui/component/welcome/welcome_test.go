package welcome

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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

func TestWelcomeBannerFullRendering(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)

	rows := m.Rows(80, 20)
	if len(rows) != 20 {
		t.Fatalf("Rows count = %d, want 20", len(rows))
	}

	view := ansi.Strip(m.View(80, 20))
	if !strings.Contains(view, "⬖") {
		t.Errorf("missing diamond logo mark in view:\n%s", view)
	}
	// Tier-driven title: fullwidth on rich tiers, spaced caps on ASCII/no-tty.
	if !strings.Contains(view, "Ｍ Ｉ Ｖ Ｉ Ａ") && !strings.Contains(view, "M    I    V    I    A") {
		t.Errorf("missing big Mivia typography in view:\n%s", view)
	}
	if !strings.Contains(view, "thinking") {
		t.Errorf("missing state legend in view:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+b:sidebar") {
		t.Errorf("missing ctrl+b:sidebar hint in view:\n%s", view)
	}
	if strings.Contains(view, "For the work that takes longer than a chat.") {
		t.Errorf("welcome banner should not show the tagline:\n%s", view)
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
	if !strings.Contains(view, "Mivia") {
		t.Errorf("compact view missing title:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+b:sidebar") {
		t.Errorf("compact view missing keybinding hint:\n%s", view)
	}
	if strings.Contains(view, "Mac Lisowski") {
		t.Errorf("compact view should not show the author credit:\n%s", view)
	}
	if strings.Contains(view, "For the work") {
		t.Errorf("compact view should not show the tagline:\n%s", view)
	}
}

func TestWelcomeASCIITierRendering(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierASCII)

	view := ansi.Strip(m.View(80, 20))
	if !strings.Contains(view, "<>") {
		t.Errorf("ASCII tier missing ASCII diamond logo:\n%s", view)
	}
	if !strings.Contains(view, "Ｍ Ｉ Ｖ Ｉ Ａ") && !strings.Contains(view, "M    I    V    I    A") {
		t.Errorf("ASCII tier missing big Mivia title:\n%s", view)
	}
	if strings.Contains(view, "Mac Lisowski") {
		t.Errorf("ASCII tier should not show the author credit:\n%s", view)
	}
	if strings.Contains(view, "For the work") {
		t.Errorf("ASCII tier should not show the tagline:\n%s", view)
	}

	compactView := ansi.Strip(m.View(80, 6))
	if !strings.Contains(compactView, "< Mivia") {
		t.Errorf("ASCII compact view missing '< Mivia' mark:\n%s", compactView)
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
