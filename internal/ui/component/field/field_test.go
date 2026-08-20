package field

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func loadTheme(t *testing.T) theme.Theme {
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
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

func lightTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-light" {
			return th
		}
	}
	t.Fatal("mivia-light theme not found")
	return theme.Theme{}
}

func TestKindTextTypesIntoTheInput(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, "name", KindText, 40)
	m.Focus()
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	if got := next.Value(); got != "h" {
		t.Errorf("got %q, want \"h\"", got)
	}
}

// TestKindTextCarriesTheThemeForeground is the same regression class as
// composer's own theme test: bubbles/textinput ships hard-coded default
// styles, and a field that forgot to restyle it would draw text in the
// library's colour on every theme.
func TestKindTextCarriesTheThemeForeground(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor, "name", KindText, 40)
	m.Focus()
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})

	want := render.Role(th, theme.TierTrueColor, theme.RoleFG).Render("h")
	if !strings.Contains(next.View(), want) {
		t.Errorf("typed text is not styled with the theme's fg role, got:\n%q", next.View())
	}
}

func TestSetThemeRestylesAFocusedKindTextField(t *testing.T) {
	dark, light := loadTheme(t), lightTheme(t)
	m := New(dark, theme.TierTrueColor, "name", KindText, 40)
	m.Focus()
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	next.SetTheme(light, theme.TierTrueColor)

	wantLight := render.Role(light, theme.TierTrueColor, theme.RoleFG).Render("h")
	if !strings.Contains(next.View(), wantLight) {
		t.Errorf("typed text not restyled to the new theme, got:\n%q", next.View())
	}
	wasDark := render.Role(dark, theme.TierTrueColor, theme.RoleFG).Render("h")
	if wasDark != wantLight && strings.Contains(next.View(), wasDark) {
		t.Errorf("old theme's foreground survived the switch, got:\n%q", next.View())
	}
}

func TestKindChoiceCyclesWithWrap(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, "mode", KindChoice, 20)
	m.SetChoices([]string{"a", "b", "c"}, "a")
	if got := m.Value(); got != "a" {
		t.Fatalf("got %q, want a", got)
	}
	m.Cycle(1)
	if got := m.Value(); got != "b" {
		t.Errorf("got %q, want b", got)
	}
	m.Cycle(1)
	m.Cycle(1)
	if got := m.Value(); got != "a" {
		t.Errorf("got %q after wrapping forward, want a", got)
	}
	m.Cycle(-1)
	if got := m.Value(); got != "c" {
		t.Errorf("got %q after wrapping backward, want c", got)
	}
}

func TestSetChoicesStartsOnActiveOrFirst(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, "mode", KindChoice, 20)
	m.SetChoices([]string{"a", "b", "c"}, "c")
	if got := m.Value(); got != "c" {
		t.Errorf("got %q, want c (the active choice)", got)
	}
	m.SetChoices([]string{"x", "y"}, "not-present")
	if got := m.Value(); got != "x" {
		t.Errorf("got %q, want x (fallback to the first choice)", got)
	}
}

func TestKindChoiceUpdateNeverTypes(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, "mode", KindChoice, 20)
	m.SetChoices([]string{"a", "b"}, "a")
	m.Focus()
	next, _ := m.Update(tea.KeyPressMsg{Text: "z", Code: 'z'})
	if got := next.Value(); got != "a" {
		t.Errorf("got %q, want a KindChoice field unaffected by a key press", got)
	}
}

func TestValidateRunsTheInjectedRule(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, "count", KindText, 20)
	wantErr := errors.New("must be positive")
	m.SetValidate(func(v string) error {
		if v == "-1" {
			return wantErr
		}
		return nil
	})
	m.SetValue("5")
	if err := m.Validate(); err != nil {
		t.Errorf("got %v, want nil for a valid value", err)
	}
	m.SetValue("-1")
	if err := m.Validate(); err != wantErr {
		t.Errorf("got %v, want %v", err, wantErr)
	}
}

func TestValidateWithNoRuleIsAlwaysValid(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor, "x", KindText, 20)
	m.SetValue("anything")
	if err := m.Validate(); err != nil {
		t.Errorf("got %v, want nil with no injected rule", err)
	}
}

func TestKindTextAtNoColourTierAddsNoColour(t *testing.T) {
	m := New(loadTheme(t), theme.TierNoTTY, "name", KindText, 40)
	m.Focus()
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	if got := next.View(); strings.Contains(got, "\x1b[38;2;") || strings.Contains(got, "\x1b[48;2;") {
		t.Errorf("no-TTY tier drew colour, got:\n%q", got)
	}
}
