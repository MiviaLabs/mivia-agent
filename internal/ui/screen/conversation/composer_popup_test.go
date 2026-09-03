package conversation

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
)

// TestSpliceRowKeepsWidthAndNeighbours: the overlay replaces only its own
// columns; what is left and right of it survives, and the width is exact.
func TestSpliceRowKeepsWidthAndNeighbours(t *testing.T) {
	base := "abcdefghij"
	got := ansi.Strip(spliceRow(base, 3, "XX"))
	if got != "abcXXfghij" {
		t.Fatalf("spliceRow = %q, want abcXXfghij", got)
	}
	short := ansi.Strip(spliceRow("ab", 4, "ZZ"))
	if short != "ab  ZZ" {
		t.Fatalf("spliceRow on a short base = %q, want ab  ZZ (padded to x)", short)
	}
}

// TestSlashMenuIsAnOverlayNotReflow: opening the slash menu must not change
// the frame's row count or move the status row. The popup is drawn over the
// rows directly above the bar and lists the matches there.
func TestSlashMenuIsAnOverlayNotReflow(t *testing.T) {
	s := sized(t, 0)
	s.SetCommands([]composer.Command{{Name: "agent", Desc: "pick the agent"}, {Name: "agents", Desc: "list agents"}})
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	next, _ = s.Update(keyMsg("hello"))
	s = next.(Screen)
	closed := strings.Split(s.View(), "\n")

	next, _ = s.Update(keyMsg("x"))
	s = next.(Screen)
	s.composer.SetValue("/a")
	if !s.composer.MenuActive() {
		t.Fatal("precondition: menu open")
	}
	open := strings.Split(s.View(), "\n")

	if len(open) != len(closed) {
		t.Fatalf("frame grew from %d to %d rows when the menu opened", len(closed), len(open))
	}
	if ansi.Strip(open[len(open)-2]) != ansi.Strip(closed[len(closed)-2]) {
		t.Errorf("status row moved or changed:\nclosed %q\nopen   %q", ansi.Strip(closed[len(closed)-2]), ansi.Strip(open[len(open)-2]))
	}
	plain := ansi.Strip(strings.Join(open, "\n"))
	if !strings.Contains(plain, "/agents") || !strings.Contains(plain, "/agent ") {
		t.Errorf("popup items missing from the frame:\n%s", plain)
	}
	// Screen rows at height 24: status 22, bottom padding 21, input 20, top
	// padding 19, popup footer 18, items 16-17. View includes the gutter
	// rows, so screen row == slice index.
	inputRow := 24 - 1 - 1 - 2
	footer := ansi.Strip(open[inputRow-2])
	if !strings.Contains(footer, "navigate") && !strings.Contains(footer, "Commands") {
		t.Errorf("expected the hint footer right above the bar, got %q", footer)
	}
}
