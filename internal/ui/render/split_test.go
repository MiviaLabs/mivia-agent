package render

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestSplitFramesTwoPanesAtTheNamedRatio(t *testing.T) {
	got := Split(loadTheme(t), theme.TierASCII, 100, 10, Left, "left", "right")
	rows := strings.Split(got, "\n")
	if len(rows) != 10 {
		t.Fatalf("%d rows, want 10 (exact height contract)", len(rows))
	}
	if strings.Count(got, "╭") != 2 {
		t.Errorf("framed %d panes, want 2", strings.Count(got, "╭"))
	}
	for i, r := range rows {
		if w := lipgloss.Width(r); w != 100 {
			t.Errorf("row %d width %d, want 100 (exact width contract)", i, w)
		}
	}
	// The left pane is SplitLeftShare of the width: on the content row,
	// "left" sits inside the first 32 columns and "right" beyond them.
	var contentRow string
	for _, r := range rows {
		if strings.Contains(r, "left") {
			contentRow = r
			break
		}
	}
	if contentRow == "" {
		t.Fatalf("no row shows the left pane's content:\n%s", got)
	}
	if i := strings.Index(contentRow, "left"); i > 32 {
		t.Errorf("left content at column %d, want inside the 30%% pane: %q", i, contentRow)
	}
	if i := strings.Index(contentRow, "right"); i < 30 {
		t.Errorf("right content at column %d, want beyond the 30%% pane: %q", i, contentRow)
	}
}

// TestSplitCapsTheLeftPaneOnWideTerminals: past SplitLeftMax the
// navigation pane stops growing and the content pane takes the rest.
func TestSplitCapsTheLeftPaneOnWideTerminals(t *testing.T) {
	got := Split(loadTheme(t), theme.TierASCII, 300, 10, Left, "left", "right")
	var contentRow string
	for _, r := range strings.Split(got, "\n") {
		if strings.Contains(r, "left") {
			contentRow = r
			break
		}
	}
	if contentRow == "" {
		t.Fatalf("no content row:\n%s", got)
	}
	if i := strings.Index(contentRow, "left"); i > SplitLeftMax+2 {
		t.Errorf("left content at column %d, want inside the %d-column cap: %q", i, SplitLeftMax, contentRow)
	}
	if i := strings.Index(contentRow, "right"); i < SplitLeftMax {
		t.Errorf("right content at column %d, want the space beyond the cap: %q", i, contentRow)
	}
}

// TestSplitDropsSurplusRowsNotScrolls: content taller than the pane is
// clipped by Split; windowing is the caller's job.
func TestSplitDropsSurplusRowsNotScrolls(t *testing.T) {
	tall := strings.Repeat("line\n", 30)
	got := Split(loadTheme(t), theme.TierASCII, 80, 6, Right, tall, tall)
	if n := strings.Count(got, "line"); n > 12 {
		t.Errorf("kept %d content rows in 6-row panes; Split must drop, not scroll", n)
	}
}
