package render

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// colAt is the display column of needle in row (strings.Index counts
// bytes, and the rule and borders are multi-byte runes).
func colAt(row, needle string) int {
	i := strings.Index(row, needle)
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(row[:i])
}

func TestSplitSeparatesWithOneRuleAndNoBoxes(t *testing.T) {
	got := Split(loadTheme(t), theme.TierASCII, 100, 10, Left, "reading", "nav")
	rows := strings.Split(got, "\n")
	if len(rows) != 10 {
		t.Fatalf("%d rows, want 10 (exact height contract)", len(rows))
	}
	for i, r := range rows {
		if w := lipgloss.Width(r); w != 100 {
			t.Errorf("row %d width %d, want 100 (exact width contract)", i, w)
		}
	}
	// The split's only frame is the vertical rule on the sidebar's left
	// edge: one rule glyph per row, and no box corners anywhere.
	if n := strings.Count(got, "│"); n != 10 {
		t.Errorf("drew %d rule glyphs, want one per row (10)", n)
	}
	if strings.Contains(got, "╭") || strings.Contains(got, "╰") {
		t.Errorf("the split frames a pane with a box; only the rule may draw:\n%s", got)
	}
	// The reading column sits left of the rule, the nav content right of
	// it.
	var contentRow string
	for _, r := range rows {
		if strings.Contains(r, "reading") {
			contentRow = r
			break
		}
	}
	if contentRow == "" {
		t.Fatalf("no row shows the panes' content:\n%s", got)
	}
	reading, _ := SplitWidths(100)
	if i := colAt(contentRow, "reading"); i > 2 {
		t.Errorf("reading content at column %d, want inside the left column: %q", i, contentRow)
	}
	if i := colAt(contentRow, "│"); i != reading {
		t.Errorf("rule at column %d, want %d (the sidebar's left edge): %q", i, reading, contentRow)
	}
	if i := colAt(contentRow, "nav"); i < reading {
		t.Errorf("nav content at column %d, want right of the rule at %d: %q", i, reading, contentRow)
	}
}

// TestSplitCapsTheNavPaneOnWideTerminals: past SplitNavMax the
// navigation pane stops growing and the reading pane takes the rest.
func TestSplitCapsTheNavPaneOnWideTerminals(t *testing.T) {
	got := Split(loadTheme(t), theme.TierASCII, 300, 10, Left, "reading", "nav")
	var contentRow string
	for _, r := range strings.Split(got, "\n") {
		if strings.Contains(r, "nav") {
			contentRow = r
			break
		}
	}
	if contentRow == "" {
		t.Fatalf("no content row:\n%s", got)
	}
	if i := colAt(contentRow, "nav"); i < SplitNavMax {
		t.Errorf("nav content at column %d, want beyond the %d-column cap: %q", i, SplitNavMax, contentRow)
	}
	if i := colAt(contentRow, "reading"); i > 2 {
		t.Errorf("reading content at column %d, want the space beyond the cap: %q", i, contentRow)
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

// TestSplitWidthsMatchesWhatSplitDraws: callers size pane content by
// SplitWidths, so the exported arithmetic and the drawn frame must agree
// - the nav share lands on the right pane, capped at SplitNavMax.
func TestSplitWidthsMatchesWhatSplitDraws(t *testing.T) {
	for _, w := range []int{100, 118, 160, 300, 400} {
		reading, nav := SplitWidths(w)
		if want := w - nav; reading != want {
			t.Errorf("width %d: reading %d, want %d", w, reading, want)
		}
		if nav > SplitNavMax {
			t.Errorf("width %d: nav %d exceeds the %d cap", w, nav, SplitNavMax)
		}
		if nav != w*SplitNavShare/100 && nav != SplitNavMax {
			t.Errorf("width %d: nav %d is neither the share nor the cap", w, nav)
		}
	}
	if reading, nav := SplitWidths(100); reading != 70 || nav != 30 {
		t.Errorf("SplitWidths(100) = %d, %d; want 70, 30", reading, nav)
	}
}

// TestSplitDialogReplacesTheReadingColumnKeepsTheNavPane: the dialog
// composes into the reading column's area at the split's exact frame
// size, and the nav pane's content stays visible beside it rather than
// hiding behind the dialog.
func TestSplitDialogReplacesTheReadingColumnKeepsTheNavPane(t *testing.T) {
	got := SplitDialog(loadTheme(t), theme.TierASCII, 120, 20, true, "a.go", "+ added line", "any key closes", "navrow")
	rows := strings.Split(got, "\n")
	if len(rows) != 20 {
		t.Fatalf("%d rows, want 20 (exact height contract)", len(rows))
	}
	reading, _ := SplitWidths(120)
	for i, r := range rows {
		if w := lipgloss.Width(r); w != 120 {
			t.Errorf("row %d width %d, want 120 (exact width contract)", i, w)
		}
	}
	var navRow string
	for _, r := range rows {
		if strings.Contains(r, "navrow") {
			navRow = r
			break
		}
	}
	if navRow == "" {
		t.Fatalf("nav pane content missing from the composed frame:\n%s", got)
	}
	if i := colAt(navRow, "navrow"); i < reading {
		t.Errorf("nav content at column %d, want inside the right pane (beyond %d): %q", i, reading, navRow)
	}
	if !strings.Contains(got, "+ added line") || !strings.Contains(got, "any key closes") {
		t.Errorf("dialog content missing from the composed frame:\n%s", got)
	}
	// The dialog is the one box; the split itself contributes only the
	// rule.
	if n := strings.Count(got, "╭"); n != 1 {
		t.Errorf("framed %d boxes, want 1 (the dialog)", n)
	}
}

// TestSplitClipsAWideRowWithTheClipMarker pins wireframes-panes.md
// section 8/14: "the renderer must clip a row wider than the panel...
// with a `~` marking the clip." clipBlock (behind Split/SplitDialog)
// previously truncated with no marker at all, so a cut row looked
// identical to a row that just happened to end there.
func TestSplitClipsAWideRowWithTheClipMarker(t *testing.T) {
	wide := strings.Repeat("x", 100)
	got := Split(loadTheme(t), theme.TierASCII, 30, 3, Left, wide, "nav")
	if !strings.Contains(got, uikitconfig.ClipMarker) {
		t.Errorf("got %q, want the clip marker %q on the truncated row", got, uikitconfig.ClipMarker)
	}
}
