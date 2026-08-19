package themepicker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func loadThemes(t *testing.T) []theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) < 2 {
		t.Fatalf("need at least 2 embedded themes for these tests, got %d", len(themes))
	}
	return themes
}

func TestNewSatisfiesAppScreen(t *testing.T) {
	var _ app.Screen = New(theme.Theme{}, theme.TierASCII, nil)
}

func TestInitIsNil(t *testing.T) {
	s := New(theme.Theme{}, theme.TierASCII, nil)
	if cmd := s.Init(); cmd != nil {
		t.Error("expected a nil Init Cmd")
	}
}

func TestViewListsThemeNames(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	got := s.View()
	for _, th := range themes {
		if !strings.Contains(got, th.Name) {
			t.Errorf("picker view missing theme %q:\n%s", th.Name, got)
		}
	}
	if !strings.Contains(got, "select a theme") || !strings.Contains(got, "[esc] cancel") {
		t.Errorf("picker view missing title/hint: %q", got)
	}
}

func TestThemeChangedMsgUpdatesScreenAndPicker(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	next, cmd := s.Update(app.ThemeChangedMsg{Theme: themes[1], Tier: theme.TierTrueColor})
	if cmd != nil {
		t.Error("expected no Cmd from a theme change")
	}
	got := next.(Screen)
	if got.Theme.Name != themes[1].Name || got.Tier != theme.TierTrueColor {
		t.Errorf("got Theme=%q Tier=%v, want Theme=%q Tier=TierTrueColor", got.Theme.Name, got.Tier, themes[1].Name)
	}
	if got.picker.Theme.Name != themes[1].Name {
		t.Errorf("got picker.Theme=%q, want %q", got.picker.Theme.Name, themes[1].Name)
	}
}

func TestEnterEmitsThemeSelectedMsg(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a Cmd on enter")
	}
	msg, ok := cmd().(app.ThemeSelectedMsg)
	if !ok {
		t.Fatalf("got %T, want app.ThemeSelectedMsg", cmd())
	}
	if msg.Name != themes[0].Name {
		t.Errorf("got %q, want the first (selected) theme %q", msg.Name, themes[0].Name)
	}
	if _, ok := next.(Screen); !ok {
		t.Errorf("got %T, want Update to return a themepicker.Screen", next)
	}
}

func TestEscEmitsPopScreenMsg(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a Cmd on esc")
	}
	if _, ok := cmd().(app.PopScreenMsg); !ok {
		t.Errorf("got %T, want app.PopScreenMsg", cmd())
	}
}

func TestNavigationKeyProducesNoCmd(t *testing.T) {
	themes := loadThemes(t)
	s := New(themes[0], theme.TierASCII, themes)
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd != nil {
		t.Error("expected no Cmd from a plain cursor movement")
	}
	got := next.View()
	if !strings.Contains(got, themes[1].Name) {
		t.Errorf("expected the cursor move reflected in the view: %q", got)
	}
}
