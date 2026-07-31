package cli

import (
	"strings"
	"testing"
)

// TestReplHelpAdvertisesEveryPlainCommand ensures classic dialog and inline
// help both list every plain-surface catalog command (no hand-maintained drift).
func TestReplHelpAdvertisesEveryPlainCommand(t *testing.T) {
	t.Parallel()
	commands := slashCommands(slashSurfacePlain, nil)
	if len(commands) == 0 {
		t.Fatal("precondition: slashSurfacePlain catalog is empty")
	}
	dialog := strings.Join(renderHelpLines(120), "\n")
	inline := renderReplHelpInline()
	for _, c := range commands {
		for _, which := range []struct {
			name string
			hay  string
		}{
			{"dialog", dialog},
			{"inline", inline},
		} {
			if !strings.Contains(which.hay, c.Name) {
				t.Errorf("%s help does not advertise catalog command %s", which.name, c.Name)
			}
		}
	}
	// Explicit pin for the known historical omission.
	if !strings.Contains(dialog, "/resume") || !strings.Contains(inline, "/resume") {
		t.Error("both help surfaces must advertise /resume")
	}
}

// TestReplHelpHasNoMojibake guards against the double-encoded arrow sequences
// that lived in the deleted slashHelp const.
func TestReplHelpHasNoMojibake(t *testing.T) {
	t.Parallel()
	inline := renderReplHelpInline()
	for _, bad := range []string{"â†‘", "â†“", "â†", "â†’"} {
		if strings.Contains(inline, bad) {
			t.Errorf("inline help contains mojibake sequence %q", bad)
		}
	}
	// Correct Unicode arrows from the shared key section must be present.
	for _, good := range []string{"↑", "↓", "←", "→"} {
		if !strings.Contains(inline, good) {
			t.Errorf("inline help missing correct arrow %q", good)
		}
	}
	// Dialog path uses the same content; spot-check arrows there too.
	dialog := strings.Join(renderHelpLines(120), "\n")
	for _, bad := range []string{"â†‘", "â†“", "â†", "â†’"} {
		if strings.Contains(dialog, bad) {
			t.Errorf("dialog help contains mojibake sequence %q", bad)
		}
	}
}
