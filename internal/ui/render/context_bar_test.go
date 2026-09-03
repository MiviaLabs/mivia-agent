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
