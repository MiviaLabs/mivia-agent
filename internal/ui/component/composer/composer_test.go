package composer

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

func TestNewIsEmptyAndFocused(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	if got := m.Value(); got != "" {
		t.Errorf("got %q, want empty value on a new composer", got)
	}
}

func TestUpdateTypesText(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	next, _ = next.Update(tea.KeyPressMsg{Text: "i", Code: 'i'})
	if got := next.Value(); got != "hi" {
		t.Errorf("got %q, want \"hi\"", got)
	}
}

func TestClearResetsValue(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	next.Clear()
	if got := next.Value(); got != "" {
		t.Errorf("got %q, want empty after Clear", got)
	}
}

func TestViewShowsPromptAndText(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	got := next.View()
	// RoleAccent is bold, and bold survives even the ASCII tier (by
	// design - see render.TestRoleBoldSurvivesNoColour), so the prompt
	// arrives wrapped in an SGR bold sequence rather than as a literal
	// prefix.
	if !strings.Contains(got, "> ") {
		t.Errorf("got %q, want the accent prompt \"> \" present", got)
	}
	if !strings.Contains(got, "h") {
		t.Errorf("got %q, want typed text present", got)
	}
}

func TestSetWidthClampsBelowPrompt(t *testing.T) {
	// A terminal narrower than the prompt itself must not produce a
	// zero or negative input width (bubbles' textinput would render
	// nothing, or panic on some paths).
	m := New(loadTheme(t), theme.TierASCII, 40)
	m.SetWidth(1)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	if got := next.Value(); got != "h" {
		t.Errorf("got %q, want the composer still usable at a clamped width", got)
	}
}

func TestSetSuggestionsDoesNotPanic(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	m.SetSuggestions([]string{"/help", "/agent"})
}
