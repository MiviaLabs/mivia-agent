package render

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// TestContextBarFillsExactlyItsBlocks: the top bar's four-cell gauge. It
// is the one caller of ContextBar, and a bar that drew a different number
// of cells than it was given would push the capsule beside it off the
// row - the fixed-width chrome ux-rules 2.7 says must not move.
func TestContextBarFillsExactlyItsBlocks(t *testing.T) {
	for _, tier := range []theme.Tier{theme.TierTrueColor, theme.Tier256, theme.TierASCII, theme.TierNoTTY} {
		for _, blocks := range []int{1, 4, 10} {
			for _, pct := range []int{0, 1, 50, 99, 100, 150} {
				got := ansi.Strip(ContextBar(pct, blocks, tier))
				if w := ansi.StringWidth(got); w != blocks {
					t.Errorf("tier %v, blocks %d, pct %d: bar is %d columns (%q)", tier, blocks, pct, w, got)
				}
			}
		}
	}
	// No blocks means no bar, not a stub.
	for _, blocks := range []int{0, -2} {
		if got := ContextBar(50, blocks, theme.TierASCII); got != "" {
			t.Errorf("blocks=%d produced %q, want an empty bar", blocks, got)
		}
	}
}

// TestContextBarUsesTheTiersOwnGlyphs: an ASCII terminal has no block
// glyphs, so the bar must degrade rather than print replacement boxes.
func TestContextBarUsesTheTiersOwnGlyphs(t *testing.T) {
	full, empty := ContextGlyphs(theme.TierASCII)
	bar := ansi.Strip(ContextBar(50, 4, theme.TierASCII))
	for _, r := range bar {
		if string(r) != full && string(r) != empty {
			t.Errorf("bar %q carries %q, which is neither the tier's full (%q) nor empty (%q) glyph",
				bar, string(r), full, empty)
		}
	}
}

// TestContextCellsClaimsNothingOnAZeroWidthBar: the split bar (the
// sidebar's floor/conversation runs) asks for a cell count before it knows
// it has room. A bar with no cells has none to claim, and the rounding
// arithmetic below would otherwise hand back a positive count for a bar
// that is not drawn - or a negative one for a negative width, which the
// caller then repeats into a string.
func TestContextCellsClaimsNothingOnAZeroWidthBar(t *testing.T) {
	for _, blocks := range []int{0, -1, -8} {
		for _, pct := range []int{0, 50, 100, 150} {
			if got := ContextCells(pct, blocks); got != 0 {
				t.Errorf("ContextCells(%d, %d) = %d, want 0", pct, blocks, got)
			}
		}
	}
	// A real bar still fills, so the zero above is the no-width arm and not
	// a gauge that never draws.
	if got := ContextCells(50, 4); got != 2 {
		t.Errorf("ContextCells(50, 4) = %d, want 2", got)
	}
	if got := ContextCells(150, 4); got != 4 {
		t.Errorf("ContextCells(150, 4) = %d, want the bar clamped to 4", got)
	}
}
