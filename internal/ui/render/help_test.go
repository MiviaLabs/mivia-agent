package render

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
)

// TestHelpIsGeneratedFromTheTable pins that the help screen has no
// second source. A hand-written help list drifts from the dispatch
// table, and the drift is invisible until a user presses a key the help
// promised.
func TestHelpIsGeneratedFromTheTable(t *testing.T) {
	th := loadTheme(t)
	rows := keymap.New(keymap.Default()).Help()
	got := plain(Help(th, theme.TierASCII, rows))

	for _, r := range rows {
		if !strings.Contains(got, r.Help) {
			t.Errorf("rendered help is missing the entry %q", r.Help)
		}
		if !strings.Contains(got, r.Keys) {
			t.Errorf("rendered help is missing the keys %q", r.Keys)
		}
	}
}

func TestHelpGroupsByContextAndAligns(t *testing.T) {
	th := loadTheme(t)
	rows := []keymap.HelpRow{
		{Context: keymap.ContextGlobal, Keys: "ctrl+t", Help: "theme"},
		{Context: keymap.ContextGlobal, Keys: "esc", Help: "cancel"},
		{Context: keymap.ContextComposer, Keys: "enter", Help: "send"},
	}
	lines := strings.Split(plain(Help(th, theme.TierASCII, rows)), "\n")

	if lines[0] != string(keymap.ContextGlobal) {
		t.Errorf("got %q, want the first context as a heading", lines[0])
	}
	// A blank line separates the groups, so the sections read apart.
	blank := false
	for _, l := range lines {
		if l == "" {
			blank = true
		}
	}
	if !blank {
		t.Error("the two contexts are not separated by a blank line")
	}
	// The help column starts at the same offset on every row of a group.
	first := strings.Index(lines[1], "theme")
	second := strings.Index(lines[2], "cancel")
	if first != second {
		t.Errorf("help column starts at %d and %d; the keys column is not padded", first, second)
	}
}

func TestHelpEmptyRows(t *testing.T) {
	if got := Help(loadTheme(t), theme.TierASCII, nil); got != "" {
		t.Errorf("got %q, want no output for an empty table", got)
	}
}
