package render

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestStreamRenderer_CoverageBranchesDirect(t *testing.T) {
	th := streamTheme(t)
	// Directly exercise advance rendersBlank poison branch
	var r StreamRenderer
	r.bestCut = 4
	// source is "# \n\n" which renders to blank line with non-empty content
	r.advance(th, theme.TierTrueColor, 80, "# \n\n")
	if !r.poisoned {
		// Try setting source directly
		r.Reset()
		r.bestCut = 10
		r.advance(th, theme.TierTrueColor, 80, "[comment]: <> (test)\n\n")
	}

	// Directly exercise tailOpensWithBlankBlock blockEnd <= blockStart or blockStart < len(s.stableContent)
	var r2 StreamRenderer
	r2.stableContent = "hello world"
	r2.blockStart = 2
	r2.blockEnd = 5
	if r2.tailOpensWithBlankBlock(th, theme.TierTrueColor, 80, "hello world") {
		t.Fatal("expected false for blockStart < len(stableContent)")
	}

	r2.blockStart = 20
	r2.blockEnd = 15
	if r2.tailOpensWithBlankBlock(th, theme.TierTrueColor, 80, "hello world") {
		t.Fatal("expected false for blockEnd <= blockStart")
	}
}
