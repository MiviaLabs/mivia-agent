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
	var contentRow string
	for _, r := range rows {
		if strings.Contains(r, "left-content") {
			contentRow = r
			break
		}
	}
	if contentRow == "" {
		t.Fatalf("no row shows the left pane's content:\n%s", got)
	}
	if i := colAt(contentRow, "left-content"); i > 2 {
		t.Errorf("left content at column %d, want inside the left pane: %q", i, contentRow)
	}
	if i := colAt(contentRow, "│"); i != 20 {
		t.Errorf("rule at column %d, want 20 (leftWidth): %q", i, contentRow)
	}
	if i := colAt(contentRow, "right-content"); i < 20 {
		t.Errorf("right content at column %d, want right of the rule at 20: %q", i, contentRow)
	}
}

// TestSplitAtPlacesTheLeftPaneOnEitherSide is really about the pane the
// caller wants first (left argument) always landing left of the rule -
// SplitAt has no separate "side" concept beyond argument order, which is
// what lets a settings screen put its nav on the left while Split keeps
// its nav on the right, from the same primitive.
func TestSplitAtHonoursFocusColour(t *testing.T) {
	th := loadTheme(t)
	leftFocus := SplitAt(th, theme.TierTrueColor, 60, 6, 15, Left, "l", "r")
	rightFocus := SplitAt(th, theme.TierTrueColor, 60, 6, 15, Right, "l", "r")
	if leftFocus == rightFocus {
		t.Error("focus side made no difference to the rendered rule colour")
	}
	focusColour := Role(th, theme.TierTrueColor, theme.RoleBorderFocus).Render("x")
	calmColour := Role(th, theme.TierTrueColor, theme.RoleBorder).Render("x")
	i := strings.Index(focusColour, "\x1b[")
	j := strings.Index(calmColour, "\x1b[")
	if i < 0 || j < 0 {
		t.Fatal("expected coloured roles at TrueColor")
	}
	// Side names which ARGUMENT has focus, not which pane is "nav": the
	// rule colours for the Right argument, matching Split's own
	// focus==Right convention (Split's nav is its right argument too).
	if !strings.Contains(leftFocus, calmColour[j:strings.IndexByte(calmColour[j:], 'm')+j+1]) {
		t.Errorf("Left focus (right argument unfocused) did not draw the calm rule: %q", leftFocus)
	}
	if !strings.Contains(rightFocus, focusColour[i:strings.IndexByte(focusColour[i:], 'm')+i+1]) {
		t.Errorf("Right focus did not draw the focus-coloured rule: %q", rightFocus)
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
