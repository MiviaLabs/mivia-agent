// Pixel canvas for high-fidelity terminal graphics.
// Uses Unicode Braille (U+2800) - 2×4 subpixels per character cell.
// No external deps; works over SSH/tmux with monospaced fonts that include braille.
package legacytui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Braille dot bit order (Unicode):
//
//	1 4
//	2 5
//	3 6
//	7 8
var brailleBits = [4][2]int{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

// pixelGrid is a dense boolean raster (true = lit).
type pixelGrid struct {
	w, h int
	pix  []bool
}

func newPixelGrid(w, h int) *pixelGrid {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return &pixelGrid{w: w, h: h, pix: make([]bool, w*h)}
}

func (g *pixelGrid) set(x, y int, on bool) {
	if x < 0 || y < 0 || x >= g.w || y >= g.h {
		return
	}
	g.pix[y*g.w+x] = on
}

func (g *pixelGrid) get(x, y int) bool {
	if x < 0 || y < 0 || x >= g.w || y >= g.h {
		return false
	}
	return g.pix[y*g.w+x]
}

// clear resets all pixels.
func (g *pixelGrid) clear() {
	for i := range g.pix {
		g.pix[i] = false
	}
}

// renderBraille packs the grid into braille characters (2 wide × 4 tall per cell).
func (g *pixelGrid) renderBraille() string {
	cols := (g.w + 1) / 2
	rows := (g.h + 3) / 4
	var b strings.Builder
	b.Grow(rows * (cols*3 + 1))
	for row := 0; row < rows; row++ {
		if row > 0 {
			b.WriteByte('\n')
		}
		for col := 0; col < cols; col++ {
			var bits int
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					if g.get(col*2+dx, row*4+dy) {
						bits |= brailleBits[dy][dx]
					}
				}
			}
			b.WriteRune(rune(0x2800 + bits))
		}
	}
	return b.String()
}

// --- Diamond rasterization (matches brand LogoMark geometry) ---
// SVG: FRAME diamond N/E/S/W, SOLID = west half triangle.

// pointInDiamond reports whether (x,y) is inside the diamond
// with vertices at (cx, top), (right, cy), (cx, bottom), (left, cy).
func pointInDiamond(x, y, cx, cy, r float64) bool {
	// Diamond = L1 ball: |x-cx|/rx + |y-cy|/ry <= 1 with rx=ry=r.
	return math.Abs(x-cx)+math.Abs(y-cy) <= r
}

// pointOnDiamondEdge reports whether pixel is on the outline (stroke).
func pointOnDiamondEdge(x, y, cx, cy, r, stroke float64) bool {
	d := math.Abs(x-cx) + math.Abs(y-cy)
	return d <= r && d >= r-stroke
}

// rasterDiamond draws the brand mark into g.
// mode:
//
//	0 = brand default: left solid + full outline
//	1 = outline only
//	2 = right solid + outline
//	3 = fill amount 0..1 from left (fill01)
//	4 = soft glow / pulse (thicker stroke + left fill)
//	5 = scan-line fill (fill01 as vertical progress)
func rasterDiamond(g *pixelGrid, mode int, fill01 float64) {
	g.clear()
	if fill01 < 0 {
		fill01 = 0
	}
	if fill01 > 1 {
		fill01 = 1
	}

	cx := float64(g.w-1) / 2
	cy := float64(g.h-1) / 2
	// Radius leaves a 1px margin.
	r := math.Min(cx, cy) - 1
	if r < 2 {
		r = 2
	}
	stroke := math.Max(1.2, r*0.08) // ~brand 2.4/24 stroke ratio

	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			fx, fy := float64(x), float64(y)
			inside := pointInDiamond(fx, fy, cx, cy, r)
			edge := pointOnDiamondEdge(fx, fy, cx, cy, r, stroke)
			left := fx <= cx
			right := fx >= cx

			on := false
			switch mode {
			case 0: // brand: west solid + outline
				on = edge || (inside && left)
			case 1: // outline only
				on = edge
			case 2: // east solid + outline
				on = edge || (inside && right)
			case 3: // horizontal fill wipe (left → right)
				cut := (cx - r) + fill01*(2*r)
				on = edge || (inside && fx <= cut)
			case 4: // pulse: thicker edge + west fill
				thick := stroke * (1.2 + 0.8*math.Sin(fill01*math.Pi*2))
				edgeT := pointOnDiamondEdge(fx, fy, cx, cy, r, thick)
				on = edgeT || (inside && left)
			case 5: // vertical scan (top → bottom)
				cut := (cy - r) + fill01*(2*r)
				on = edge || (inside && fy <= cut)
			case 6: // sparkle: brand + dithered noise on fill edge
				on = edge || (inside && left)
				if inside && !left && edge {
					on = true
				}
				// Leading edge sparkle band
				cut := cx + (fill01-0.5)*r*0.4
				if inside && math.Abs(fx-cut) < 1.5 && (x+y)%3 == 0 {
					on = true
				}
			case 7: // breathing: radius pulses via fill01, both halves lit
				rScale := 0.88 + 0.17*fill01 // 0.88 → 1.05
				rBreath := r * rScale
				insideB := pointInDiamond(fx, fy, cx, cy, rBreath)
				edgeB := pointOnDiamondEdge(fx, fy, cx, cy, rBreath, stroke)
				on = edgeB || insideB
			default:
				on = edge || (inside && left)
			}
			if on {
				g.set(x, y, true)
			}
		}
	}
}

// styleBrailleFrame colors and optionally centers a multi-line braille block.
func styleBrailleFrame(art string, width int, color string) string {
	if color == "" {
		color = "15"
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
	lines := strings.Split(art, "\n")
	for i, ln := range lines {
		lines[i] = style.Render(ln)
	}
	block := strings.Join(lines, "\n")
	if width > 0 {
		block = lipgloss.PlaceHorizontal(width, lipgloss.Center, block)
	}
	return block
}
