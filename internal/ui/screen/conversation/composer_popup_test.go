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

// TestComposerPopupNeverCoversTheTopbar: on a terminal too short to hold
// the whole popup above the bar, the popup is clipped at the top bar's
// edge - the transcript rows and the margin are its ceiling - rather than
// painted over the bar's own row.
func TestComposerPopupNeverCoversTheTopbar(t *testing.T) {
	s := sized(t, 0)
	var cmds []composer.Command
	for _, n := range []string{"agent", "agents", "approve", "attach", "audit", "auto", "away"} {
		cmds = append(cmds, composer.Command{Name: n, Desc: "does " + n})
	}
	s.SetCommands(cmds)
	// Height 13: gutter 0, topbar 1, margin 2, transcript 3-7 (5 rows),
	// bar 8-10, status 11, gutter 12. The popup wants 9 rows (padding,
	// six items, count, footer) and only 6 rows sit above the bar.
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 13})
	s = next.(Screen)
	closed := strings.Split(s.View(), "\n")
	s.composer.SetValue("/a")
	if !s.composer.MenuActive() || len(s.composer.Popup()) <= s.transcriptHeight()+1 {
		t.Fatalf("precondition: menu open with a popup taller than the %d rows above the bar", s.transcriptHeight()+1)
	}
	open := strings.Split(s.View(), "\n")
	if len(open) != len(closed) {
		t.Fatalf("frame grew from %d to %d rows", len(closed), len(open))
	}
	if ansi.Strip(open[1]) != ansi.Strip(closed[1]) {
		t.Errorf("the top bar row must not be painted over:\nclosed %q\nopen   %q", ansi.Strip(closed[1]), ansi.Strip(open[1]))
	}
	if !strings.Contains(ansi.Strip(open[2]), "/a") {
		t.Errorf("the popup should still reach the margin row under the top bar, got %q", ansi.Strip(open[2]))
	}
	if !strings.Contains(ansi.Strip(open[7]), "navigate") && !strings.Contains(ansi.Strip(open[7]), "Commands") {
		t.Errorf("the popup footer should sit right above the bar, got %q", ansi.Strip(open[7]))
	}
}
