package clichat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestDialogCompositorExactCanvas(t *testing.T) {
	base := normalizeCanvas("one\ntwo", 8, 4)
	if len(base) != 4 {
		t.Fatalf("base rows=%d, want 4", len(base))
	}
	for i, row := range base {
		if got := ansi.StringWidth(row); got != 8 {
			t.Fatalf("base row %d width=%d, want 8: %q", i, got, row)
		}
	}
	got := strings.Split(OverlayAt("one\ntwo", "X\nY", Rect{X: 2, Y: 1, W: 2, H: 2}, 8, 4), "\n")
	if len(got) != 4 {
		t.Fatalf("composited rows=%d, want 4", len(got))
	}
	for i, row := range got {
		if width := ansi.StringWidth(row); width != 8 {
			t.Fatalf("composited row %d width=%d, want 8: %q", i, width, row)
		}
	}
	if plain := stripANSI(got[1]); plain != "twX     " {
		t.Fatalf("panel cells replaced wrong region: %q", plain)
	}
}

func TestDialogANSISeamsPreserveStyles(t *testing.T) {
	line := "ab\x1b[1;31mcolored\x1b[22;39mcd"
	part := sliceANSI(line, 4, 10)
	if !strings.Contains(part, "\x1b[1;31m") || !strings.Contains(part, "\x1b[0m") {
		t.Fatalf("slice did not carry/reset active SGR: %q", part)
	}
	panel := "P\x1b[39mQ"
	view := OverlayAt(line, panel, Rect{X: 4, Y: 0, W: 2, H: 1}, 12, 1)
	if got := ansi.StringWidth(view); got != 12 {
		t.Fatalf("seamed row width=%d, want 12: %q", got, view)
	}
	if strings.Contains(stripANSI(view), "colored") {
		t.Fatalf("panel did not replace base cells: %q", stripANSI(view))
	}
	wide := sliceANSI("\x1b[38;2;1;2;3m🙂text\x1b[39m", 2, 6)
	if !strings.Contains(wide, "38;2;1;2;3m") || !strings.Contains(wide, "0m") {
		t.Fatalf("24-bit SGR state was not carried across slice: %q", wide)
	}
}

// TestDialogCompositorUnterminatedSGRCrossesSeam pins the popup prerequisite:
// transcript renderers may leave a style active at a splice boundary. The
// shared compositor must preserve both visible sides of the cut and close the
// carried style inside its own canvas row.
func TestDialogCompositorUnterminatedSGRCrossesSeam(t *testing.T) {
	base := "abcd\x1b[31mEFGH"
	view := OverlayAt(base, "XX", Rect{X: 5, Y: 0, W: 2, H: 1}, 8, 1)
	if got := stripANSI(view); got != "abcdEXXH" {
		t.Fatalf("visible splice = %q, want %q", got, "abcdEXXH")
	}
	if got := ansi.StringWidth(view); got != 8 {
		t.Fatalf("visible width = %d, want 8: %q", got, view)
	}
	if !strings.HasSuffix(view, sgrReset) {
		t.Fatalf("unterminated source SGR leaked past canvas row: %q", view)
	}
}

func TestDialogCompositorPreservesCJKCellsAtSeam(t *testing.T) {
	view := OverlayAt("甲乙丙丁", "中", Rect{X: 2, Y: 0, W: 2, H: 1}, 8, 1)
	if got := stripANSI(view); got != "甲中丙丁" {
		t.Fatalf("visible CJK splice = %q, want %q", got, "甲中丙丁")
	}
	if got := ansi.StringWidth(view); got != 8 {
		t.Fatalf("CJK width = %d, want 8: %q", got, view)
	}
}

func TestDialogCompositorReplacesFullWidthTranscriptRow(t *testing.T) {
	view := OverlayAt("abcdefgh", "甲乙丙丁", Rect{X: 0, Y: 0, W: 8, H: 1}, 8, 1)
	if got := stripANSI(view); got != "甲乙丙丁" {
		t.Fatalf("full-width replacement = %q, want %q", got, "甲乙丙丁")
	}
	if got := ansi.StringWidth(view); got != 8 {
		t.Fatalf("full-width row = %d, want 8: %q", got, view)
	}
}

func TestDialogViewsStayWithinTerminalBoundsCompositor(t *testing.T) {
	for _, size := range []struct{ w, h int }{{1, 1}, {10, 4}, {20, 6}, {39, 10}} {
		view := OverlayAt("base", "panel", Rect{X: 0, Y: 0, W: size.w, H: size.h}, size.w, size.h)
		rows := strings.Split(view, "\n")
		if len(rows) != size.h {
			t.Fatalf("%dx%d rows=%d", size.w, size.h, len(rows))
		}
	}
	if got := ansi.StringWidth(normalizeCanvas("🙂", 1, 1)[0]); got != 1 {
		t.Fatalf("narrow wide-grapheme fallback width=%d, want 1", got)
	}
	if got := stripANSI(normalizeCanvas("🙂", 1, 1)[0]); got != "�" {
		t.Fatalf("narrow wide-grapheme policy=%q, want explicit replacement", got)
	}
}
