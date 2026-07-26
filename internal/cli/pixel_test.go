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

func TestDiamondAnimFramesFidelity(t *testing.T) {
	frames := diamondAnimFrames(32, 32, 12)
	if len(frames) != 12 {
		t.Fatalf("got %d frames", len(frames))
	}
	// Every frame has braille + multiple lines
	for i, f := range frames {
		if !strings.Contains(f, "\n") {
			t.Fatalf("frame %d not multi-line", i)
		}
		has := false
		for _, r := range f {
			if r >= 0x2800 && r <= 0x28FF && r != 0x2800 {
				has = true
				break
			}
		}
		if !has {
			t.Fatalf("frame %d has no lit braille dots", i)
		}
	}
	// Consecutive frames should not all be identical (animation moves)
	same := 0
	for i := 1; i < len(frames); i++ {
		if frames[i] == frames[i-1] {
			same++
		}
	}
	if same == len(frames)-1 {
		t.Fatal("all frames identical — animation dead")
	}
}

func TestLogoFrameRender(t *testing.T) {
	out := renderLogoFrame(0, 60)
	if out == "" {
		t.Fatal("empty logo")
	}
	if !strings.Contains(renderWordmark(40), "MIVIA") {
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
