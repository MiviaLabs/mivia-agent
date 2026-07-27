// Brand mark animation for the TUI welcome screen.
//
// Fidelity: Unicode Braille pixel canvas (2×4 subpixels per cell) — same
// technique as drawille / pixterm-class renderers, zero external deps.
// Geometry matches go-mivia LogoMark (logo.tsx):
//
//	FRAME M12 3 L21 12 L12 21 L3 12 Z
//	SOLID M12 3 L3 12 L12 21 Z  — west/left half filled.
package cli

import (
	"math"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

var (
	logoOnce     sync.Once
	logoHiFrames []string // full-res braille (~16×8 cells)
)

const (
	logoHiPixelW = 48 // 24 braille cols
	logoHiPixelH = 48 // 12 braille rows
	logoNFrames  = 24 // smooth loop @ ~140ms ≈ 3.4s cycle
)

func ensureLogoFrames() {
	logoOnce.Do(func() {
		logoHiFrames = diamondAnimFrames(logoHiPixelW, logoHiPixelH, logoNFrames)
	})
}

func logoFrameCount() int {
	ensureLogoFrames()
	return len(logoHiFrames)
}

// renderLogoFrame returns a high-fidelity braille diamond for the animation frame.
func renderLogoFrame(frame int, width int) string {
	return renderLogoFrameColor(frame, width, brandColorWelcome)
}

// renderLogoFrameColor is the same mark with a phase color (welcome/work).
func renderLogoFrameColor(frame int, width int, color string) string {
	ensureLogoFrames()
	if len(logoHiFrames) == 0 {
		return ""
	}
	if frame < 0 {
		frame = 0
	}
	art := logoHiFrames[frame%len(logoHiFrames)]
	return styleBrailleFrame(art, width, color)
}

// renderWordmark returns the text wordmark fallback (MIVIA).
func renderWordmark(width int) string {
	word := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF")).
		Bold(true).
		Render("MIVIA")
	if width > 0 {
		return lipgloss.PlaceHorizontal(width, lipgloss.Center, word)
	}
	return word
}

// ─── Dot-matrix wordmark (CRT welcome screen) ─────────────────────────

// letterPixelData is a 6-col × 8-row pixel map for a single dot-matrix letter.
// Each string is one row of 6 chars; 'X' marks a lit pixel, '.' is unlit.
// 6×8 pixels = 3 braille cols × 2 braille rows.
type letterPixelData [8]string

var wordmarkLetters = []struct {
	rune
	letterPixelData
}{
	{'M', letterPixelData{
		"X....X",
		"XX..XX",
		"X.XX.X",
		"X.XX.X",
		"X....X",
		"X....X",
		"X....X",
		"X....X",
	}},
	{'I', letterPixelData{
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
	}},
	{'V', letterPixelData{
		"X....X",
		"X....X",
		"X....X",
		"X....X",
		".X..X.",
		"..XX..",
		"......",
		"......",
	}},
	{'I', letterPixelData{
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
	}},
	{'A', letterPixelData{
		"..XX..",
		"..XX..",
		".X..X.",
		".X..X.",
		"XXXXXX",
		"X....X",
		"X....X",
		"X....X",
	}},
}

var (
	wordmarkOnce       sync.Once
	letterBrailleMIVIA [5][2][3]rune // pre-computed braille per MIVIA letter [idx][row][col]
)

// ensureBrailleWordmark pre-computes the 6×8 pixel letters into 2×3 braille rune matrices.
func ensureBrailleWordmark() {
	wordmarkOnce.Do(func() {
		for li, ld := range wordmarkLetters {
			for br := 0; br < 2; br++ {
				for bc := 0; bc < 3; bc++ {
					letterBrailleMIVIA[li][br][bc] = pixelDataToBraille(ld.letterPixelData, br, bc)
				}
			}
		}
	})
}

// pixelDataToBraille converts a 6×8 pixel letter map at braille position (br,bc)
// into a single braille rune (U+2800–U+28FF).
func pixelDataToBraille(ld letterPixelData, br, bc int) rune {
	var bits int
	for dy := 0; dy < 4; dy++ {
		py := br*4 + dy
		if py >= 8 {
			continue
		}
		row := ld[py]
		for dx := 0; dx < 2; dx++ {
			px := bc*2 + dx
			if px >= 6 || px >= len(row) {
				continue
			}
			if row[px] == 'X' {
				bits |= brailleBit(dx, dy)
			}
		}
	}
	return rune(0x2800 + bits)
}

// brailleBit returns the braille dot bit for pixel offset (dx,dy) within a 2×4 cell.
func brailleBit(dx, dy int) int {
	switch {
	case dx == 0 && dy == 0:
		return 0x01
	case dx == 1 && dy == 0:
		return 0x08
	case dx == 0 && dy == 1:
		return 0x02
	case dx == 1 && dy == 1:
		return 0x10
	case dx == 0 && dy == 2:
		return 0x04
	case dx == 1 && dy == 2:
		return 0x20
	case dx == 0 && dy == 3:
		return 0x40
	case dx == 1 && dy == 3:
		return 0x80
	default:
		return 0
	}
}

// letterBrightness returns a brightness factor [0.3, 1.0] for a letter in the
// M-I-V-I-A wordmark at the given animation frame. Creates a continuous
// KITT-scanner wave sweeping left-to-right.
func letterBrightness(frame, letterIndex int) float64 {
	phase := float64(letterIndex) * 1.256 // ~72° offset per letter
	t := float64(frame) * 0.3             // speed factor
	return 0.65 + 0.35*math.Sin(t+phase)
}

// brightnessColor maps a brightness value [0.3, 1.0] to an ANSI 256-color string.
func brightnessColor(b float64) string {
	switch {
	case b >= 0.85:
		return "15" // bright white (peak glow)
	case b >= 0.65:
		return "250" // light gray
	case b >= 0.45:
		return "244" // mid gray
	default:
		return "236" // dim (phosphor decay)
	}
}

// renderWordmarkBraille renders the MIVIA dot-matrix wordmark as animated braille.
// frame: animation frame index (for glow wave); width: horizontal centering target.
// Returns a multi-line string (2 braille lines).
func renderWordmarkBraille(frame, width int) string {
	lines := renderWordmarkBrailleLines(frame)
	out := lines[0] + "\n" + lines[1]
	if width > 0 {
		out = lipgloss.PlaceHorizontal(width, lipgloss.Center, out)
	}
	return out
}

// renderWordmarkBrailleLines returns the 2 braille lines for MIVIA (raw, uncentered).
func renderWordmarkBrailleLines(frame int) [2]string {
	ensureBrailleWordmark()
	var rows [2]strings.Builder
	for li := 0; li < 5; li++ {
		b := letterBrightness(frame, li)
		col := brightnessColor(b)
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(col)).Bold(true)
		for br := 0; br < 2; br++ {
			for bc := 0; bc < 3; bc++ {
				rows[br].WriteString(style.Render(string(letterBrailleMIVIA[li][br][bc])))
			}
			if li < 4 {
				rows[br].WriteString(" ")
			}
		}
	}
	return [2]string{rows[0].String(), rows[1].String()}
}

// renderWordmarkBrailleStatic returns the wordmark without glow animation (static).
func renderWordmarkBrailleStatic(width int) string {
	return renderWordmarkBraille(0, width)
}

// logoStaticBrand is frame 0 only (left-solid brand) for tests / reduced motion.
func logoStaticBrand(width int) string {
	g := newPixelGrid(logoHiPixelW, logoHiPixelH)
	rasterDiamond(g, 0, 0)
	return styleBrailleFrame(g.renderBraille(), width, "15")
}

// legacy coarse frames kept only if braille unavailable in extreme environments —
// not used in normal path. Exported for test comparison.
var logoFramesLegacy = []string{
	strings.Join([]string{
		`      /\`,
		`     /██\`,
		`    /████\`,
		`   /██████\`,
		`  /████    \`,
		` /████      \`,
		`  \████    /`,
		`   \██████/`,
		`    \████/`,
		`     \██/`,
		`      \/`,
	}, "\n"),
}
