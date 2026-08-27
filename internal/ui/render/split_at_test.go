package render

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// TestSplitAtSizesTheLeftPaneExactly: leftWidth is the LEFT pane's content
// width, not including the rule column - the same convention Split's own
// "reading" already uses, so Split can become a one-line caller.
func TestSplitAtSizesTheLeftPaneExactly(t *testing.T) {
	got := SplitAt(loadTheme(t), theme.TierASCII, 100, 10, 20, Left, "left-content", "right-content")
	rows := strings.Split(got, "\n")
	if len(rows) != 10 {
		t.Fatalf("%d rows, want 10", len(rows))
	}
	for i, r := range rows {
		if w := lipgloss.Width(r); w != 100 {
			t.Errorf("row %d width %d, want 100", i, w)
		}
	}
	var leftRow, rightRow string
	for _, r := range rows {
		if leftRow == "" && strings.Contains(r, "left-content") {
			leftRow = r
		}
		if rightRow == "" && strings.Contains(r, "right-content") {
			rightRow = r
		}
	}
	if leftRow == "" || rightRow == "" {
		t.Fatalf("rows missing panes' content:\n%s", got)
	}
	if i := colAt(leftRow, "left-content"); i > 2 {
		t.Errorf("left content at column %d, want inside the left pane: %q", i, leftRow)
	}
	if i := colAt(rightRow, "right-content"); i < 21 {
		t.Errorf("right content at column %d, want right of the gutter at 21: %q", i, rightRow)
	}
}

// TestSplitAtSubtleBackground asserts that the right pane receives RoleBGSubtle background.
func TestSplitAtSubtleBackground(t *testing.T) {
	th := loadTheme(t)
	got := SplitAt(th, theme.TierTrueColor, 60, 6, 15, Left, "l", "r")
	subtleBG := strings.TrimSuffix(FillBG(th, theme.TierTrueColor, theme.RoleBGSubtle, ""), "\x1b[m")
	if subtleBG != "" && !strings.Contains(got, subtleBG) {
		t.Errorf("SplitAt missing RoleBGSubtle background in right pane (%q):\n%q", subtleBG, got)
	}
}

// TestSplitAtDropsSurplusRowsNotScrolls mirrors Split's own contract:
// content taller than the pane clips, it never scrolls.
func TestSplitAtDropsSurplusRowsNotScrolls(t *testing.T) {
	tall := strings.Repeat("line\n", 30)
	got := SplitAt(loadTheme(t), theme.TierASCII, 80, 6, 20, Right, tall, tall)
	if n := strings.Count(got, "line"); n > 12 {
		t.Errorf("kept %d content rows in 6-row panes; SplitAt must drop, not scroll", n)
	}
}

// TestSplitIsSplitAtWithTheSharedWidths proves Split is now a thin
// wrapper: identical output to calling SplitAt directly with SplitWidths'
// reading share as leftWidth.
func TestSplitIsSplitAtWithTheSharedWidths(t *testing.T) {
	th := loadTheme(t)
	for _, w := range []int{60, 100, 118, 300} {
		reading, _ := SplitWidths(w)
		want := SplitAt(th, theme.TierTrueColor, w, 12, reading, Left, "reading-pane", "nav-pane")
		got := Split(th, theme.TierTrueColor, w, 12, Left, "reading-pane", "nav-pane")
		if got != want {
			t.Errorf("width %d: Split diverged from SplitAt(reading=%d)", w, reading)
		}
	}
}
