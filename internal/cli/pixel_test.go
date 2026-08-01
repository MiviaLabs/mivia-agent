package cli

import (
	"strings"
	"testing"
	"unicode"
)

func TestBrailleRenderNonEmpty(t *testing.T) {
	g := newPixelGrid(16, 16)
	for i := 0; i < 16; i++ {
		g.set(i, i, true)
	}
	out := g.renderBraille()
	if out == "" {
		t.Fatal("empty braille")
	}
	// Must contain braille block characters (U+2800+)
	hasBraille := false
	for _, r := range out {
		if r >= 0x2800 && r <= 0x28FF {
			hasBraille = true
			break
		}
	}
	if !hasBraille {
		t.Fatalf("expected braille runes, got %q", out)
	}
}

func TestRasterDiamondBrandLeftSolid(t *testing.T) {
	g := newPixelGrid(40, 40)
	rasterDiamond(g, 0, 0)
	cx := 19
	// Left interior should be lit; far-right interior less so (outline only).
	leftOn := 0
	rightOn := 0
	for y := 10; y < 30; y++ {
		if g.get(cx-6, y) {
			leftOn++
		}
		if g.get(cx+6, y) {
			rightOn++
		}
	}
	if leftOn < 5 {
		t.Fatalf("left half should be solid, leftOn=%d", leftOn)
	}
	if rightOn >= leftOn {
		t.Fatalf("right should be outline-only-ish, leftOn=%d rightOn=%d", leftOn, rightOn)
	}
}

func TestWelcomeFramesKeepOutlineWhileAnimating(t *testing.T) {
	// The splash mark is the idle state of the state-logo engine: the interior
	// animates (clockwork facet light - a deliberate product decision that
	// supersedes the earlier static-outline splash), but the outline diamond
	// is present on every frame so the mark itself never flickers or vanishes.
	outline := newPixelGrid(stateLogoPxW, stateLogoPxH)
	rasterDiamond(outline, 1, 0)
	outlineDots := 0
	for _, r := range outline.renderBraille() {
		if r > 0x2800 && r <= 0x28FF {
			outlineDots++
		}
	}

	frames := stateLogoFrames(phaseWelcome)
	for i, f := range frames {
		plain := stripANSI(f)
		if !strings.Contains(plain, "\n") {
			t.Fatalf("frame %d not multi-line", i)
		}
		lit := 0
		for _, r := range plain {
			if r > 0x2800 && r <= 0x28FF {
				lit++
			}
		}
		// Interior shading only ever adds cells on top of the outline.
		if lit < outlineDots {
			t.Fatalf("frame %d has %d lit cells, outline alone needs %d", i, lit, outlineDots)
		}
	}
}

func TestLogoFrameRender(t *testing.T) {
	out := renderLogoFrame(0, 60)
	if out == "" {
		t.Fatal("empty logo")
	}
	if !strings.Contains(renderWordmark(40), "mivia") {
		t.Fatal("wordmark")
	}
	// Centered block should be reasonably wide
	lines := strings.Split(out, "\n")
	if len(lines) < 6 {
		t.Fatalf("too few rows: %d", len(lines))
	}
}

func TestBrailleOnlyPrintable(t *testing.T) {
	g := newPixelGrid(8, 8)
	rasterDiamond(g, 0, 0)
	for _, r := range g.renderBraille() {
		if r == '\n' {
			continue
		}
		if !unicode.IsPrint(r) && r < 0x2800 {
			t.Fatalf("non-printable %U", r)
		}
	}
}
