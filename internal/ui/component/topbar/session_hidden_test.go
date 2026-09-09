package topbar

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// SetSessionHidden is what the sidebar uses to take ownership of the
// model identity while it is open, so the same fact is not stated twice
// on one screen. Both halves matter: the capsule AND the context badge
// go together, and the double-click target goes with them - a bar that
// kept reporting model bounds while drawing no capsule would open the
// model picker from a blank stretch of the row.

func hiddenBar(t *testing.T) Model {
	t.Helper()
	return New(loadTheme(t), theme.TierASCII,
		ports.ModelInfo{Name: "some-model", Provider: "zai", ContextWindow: 100_000},
		ports.Usage{InputTokens: 40_000}, 120)
}

func TestHidingTheSessionRemovesTheCapsuleTheBadgeAndTheClickTarget(t *testing.T) {
	m := hiddenBar(t)

	shown := ansi.Strip(m.View())
	if !strings.Contains(shown, "some-model") {
		t.Fatalf("precondition: the capsule names the model:\n%s", shown)
	}
	if _, _, ok := m.ModelBounds(); !ok {
		t.Fatal("precondition: the visible capsule reports a click target")
	}

	m.SetSessionHidden(true)
	hidden := ansi.Strip(m.View())
	if strings.Contains(hidden, "some-model") {
		t.Errorf("the capsule survived hiding:\n%s", hidden)
	}
	if strings.Contains(hidden, "%") {
		t.Errorf("the context badge survived hiding:\n%s", hidden)
	}
	if _, _, ok := m.ModelBounds(); ok {
		t.Error("a hidden capsule still reports a click target; a double-click would open the picker from blank space")
	}

	// And it comes back: the sidebar closing hands the identity back.
	m.SetSessionHidden(false)
	if back := ansi.Strip(m.View()); !strings.Contains(back, "some-model") {
		t.Errorf("the capsule did not return when the session was shown again:\n%s", back)
	}
}

// TestInfoReportsTheSessionItWasGiven: the screen refreshes the bar's
// identity from the conversation and reads it back to label other
// surfaces, so the accessor has to return what was set.
func TestInfoReportsTheSessionItWasGiven(t *testing.T) {
	want := ports.ModelInfo{Name: "some-model", Provider: "zai", ContextWindow: 100_000}
	m := New(loadTheme(t), theme.TierASCII, want, ports.Usage{}, 80)
	if got := m.Info(); got != want {
		t.Errorf("Info() = %+v, want %+v", got, want)
	}
}
