package render

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestStreamRenderer_CoverageBranches(t *testing.T) {
	th := streamTheme(t)
	// Exercise rendersBlank where strings.TrimSpace(src) == ""
	if rendersBlank("   \n\t  ", "") {
		t.Fatal("expected whitespace-only src to not render blank")
	}

	// Exercise compose trimmedHead == "" and trimmedTail == ""
	if got := compose("", "tail"); got != "tail" {
		t.Fatalf("expected tail, got %q", got)
	}
	if got := compose("head", ""); got != "head" {
		t.Fatalf("expected head, got %q", got)
	}

	// Exercise advance with a block that renders blank
	var r StreamRenderer
	// A block like an empty heading or comment that triggers rendersBlank during advance
	r.stableContent = ""
	r.bestCut = 4
	// Call advance directly or simulate
	r.advance(th, theme.TierTrueColor, 80, "[comment]: <> (hello)\n\nnext\n")

	// Exercise tailOpensWithBlankBlock with blockStart < len(s.stableContent) or blockEnd <= blockStart
	var r2 StreamRenderer
	r2.Render(th, theme.TierTrueColor, 80, "para one\n\npara two\n\n")
	_ = r2.tailOpensWithBlankBlock(th, theme.TierTrueColor, 80, "para one\n\npara two\n\n")

	// Exercise blockRendersBlank with blockEnd <= blockStart
	var r3 StreamRenderer
	r3.blockEnd = 0
	r3.blockStart = 5
	if r3.blockRendersBlank(th, theme.TierTrueColor, 80, "test") {
		t.Fatal("expected false")
	}
}
