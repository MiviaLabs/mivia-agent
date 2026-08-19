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
	if !strings.Contains(view, "█▀▄▀█") {
		t.Errorf("missing enlarged Mivia Agent typography in view:\n%s", view)
	}
	if !strings.Contains(view, "⬖") {
		t.Errorf("missing diamond logo mark in view:\n%s", view)
	}
	if !strings.Contains(view, "Mac Lisowski") {
		t.Errorf("missing author credit in view:\n%s", view)
	}
	if !strings.Contains(view, "MIT License") {
		t.Errorf("missing MIT license in view:\n%s", view)
	}
	if !strings.Contains(view, "thinking") {
		t.Errorf("missing state legend in view:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+n:sidebar") {
		t.Errorf("missing ctrl+n:sidebar hint in view:\n%s", view)
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
	if !strings.Contains(view, "Mivia Agent") {
		t.Errorf("compact view missing title:\n%s", view)
	}
	if !strings.Contains(view, "Mac Lisowski") {
		t.Errorf("compact view missing author:\n%s", view)
	}
}

func TestWelcomeASCIITierRendering(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierASCII)

	view := ansi.Strip(m.View(80, 20))
	if !strings.Contains(view, "<>") {
		t.Errorf("ASCII tier missing ASCII diamond logo:\n%s", view)
	}
	if !strings.Contains(view, "M I V I A   A G E N T") {
		t.Errorf("ASCII tier missing ASCII Mivia Agent title:\n%s", view)
	}
	if !strings.Contains(view, "Mac Lisowski") {
		t.Errorf("ASCII tier missing author:\n%s", view)
	}
}
