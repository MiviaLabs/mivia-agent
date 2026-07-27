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
	logoLoFrames []string // compact for short terminals
)

const (
	logoHiPixelW = 48 // 24 braille cols
	logoHiPixelH = 48 // 12 braille rows
	logoLoPixelW = 28
	logoLoPixelH = 28
	logoNFrames  = 24 // smooth loop @ ~140ms ≈ 3.4s cycle
)

func ensureLogoFrames() {
	logoOnce.Do(func() {
		logoHiFrames = diamondAnimFrames(logoHiPixelW, logoHiPixelH, logoNFrames)
		logoLoFrames = diamondAnimFrames(logoLoPixelW, logoLoPixelH, logoNFrames)
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

// compactLogoFrame is lower resolution for short terminals (height < ~22).
func compactLogoFrame(frame int, width int) string {
	return compactLogoFrameColor(frame, width, brandColorWelcome)
}

func compactLogoFrameColor(frame int, width int, color string) string {
	ensureLogoFrames()
	if len(logoLoFrames) == 0 {
		return renderLogoFrameColor(frame, width, color)
	}
	if frame < 0 {
		frame = 0
	}
	art := logoLoFrames[frame%len(logoLoFrames)]
	return styleBrailleFrame(art, width, color)
}

// renderWordmark returns the MIVIA word under the mark.
func renderWordmark(width int) string {
	word := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF")).
		Bold(true).
		Render("MIVIA")
	sub := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#AAAAAA")).
		Bold(true).
		Render("agent")
	line := word + "  " + sub
	if width > 0 {
		return lipgloss.PlaceHorizontal(width, lipgloss.Center, line)
	}
	return line
}

// ─── Dot-matrix wordmark (CRT welcome screen) ─────────────────────────

// letterPixelData is a 6-col × 8-row pixel map for a single dot-matrix letter.
// Each string is one row of 6 chars; 'X' marks a lit pixel, '.' is unlit.
// 6×8 pixels = 3 braille cols × 2 braille rows.
type letterPixelData [8]string

// wordmarkLetters holds dot-matrix pixel maps for M-I-V-I-A.
// Each letter is 6 cols × 8 rows = 3 braille cols × 2 braille rows.
// Bolder versions with thicker strokes and more filled pixels.
var wordmarkLetters = []struct {
	rune
	letterPixelData
}{
	{'M', letterPixelData{
		"XX..XX",
		"XXXXXX",
		"XXXXXX",
		"XXXXXX",
		"X.XX.X",
		"X.XX.X",
		"XX..XX",
		"XX..XX",
	}},
	{'I', letterPixelData{
		"..XX..",
		"..XX..",
		"..XX..",
		"..XX..",
		"..XX..",
		"..XX..",
		"..XX..",
		"..XX..",
	}},
	{'V', letterPixelData{
		"XX..XX",
		"XX..XX",
		"XX..XX",
		"XX..XX",
		".XXXX.",
		"..XX..",
		"..XX..",
		"..XX..",
	}},
	{'I', letterPixelData{
		"..XX..",
		"..XX..",
		"..XX..",
		"..XX..",
		"..XX..",
		"..XX..",
		"..XX..",
		"..XX..",
	}},
	{'A', letterPixelData{
		"..XX..",
		"..XX..",
		"XX..XX",
		"XX..XX",
		"XXXXXX",
		"XXXXXX",
		"XX..XX",
		"XX..XX",
	}},
}

// wordmarkSubLetters holds dot-matrix pixel maps for A-G-E-N-T subtitle.
// Same 6×8 pixel format as the main wordmark letters, uppercase.
var wordmarkSubLetters = []struct {
	rune
	letterPixelData
}{
	{'A', letterPixelData{
		"..XX..",
		"..XX..",
		"XX..XX",
		"XX..XX",
		"XXXXXX",
		"XXXXXX",
		"XX..XX",
		"XX..XX",
	}},
	{'G', letterPixelData{
		".XXXX.",
		"X....X",
		"X.....",
		"X..XX.",
		"X....X",
		"X....X",
		".XXXX.",
		"......",
	}},
	{'E', letterPixelData{
		"XXXXXX",
		"X.....",
		"X.....",
		"XXXXXX",
		"X.....",
		"X.....",
		"XXXXXX",
		"......",
	}},
	{'N', letterPixelData{
		"X....X",
		"XX...X",
		"XX.X.X",
		"X.XX.X",
		"X.X.XX",
		"X...XX",
		"X....X",
		"......",
	}},
	{'T', letterPixelData{
		"XXXXXX",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
		"..X...",
	}},
}

var (
	wordmarkOnce       sync.Once
	letterBrailleMIVIA [5][2][3]rune // pre-computed braille per MIVIA letter [idx][row][col]
	letterBrailleAGENT [5][2][3]rune // pre-computed braille per AGENT letter [idx][row][col]
)

// ensureBrailleWordmark pre-computes the 6×8 pixel letters into 2×3 braille rune matrices
// for both MIVIA and AGENT wordmarks.
func ensureBrailleWordmark() {
	wordmarkOnce.Do(func() {
		// Pre-compute MIVIA letters.
		for li, ld := range wordmarkLetters {
			for br := 0; br < 2; br++ { // braille row (0–1)
				for bc := 0; bc < 3; bc++ { // braille col (0–2)
					// Each braille cell covers 2×4 pixels.
					// Pixel (x,y) where x ∈ [bc*2, bc*2+1], y ∈ [br*4, br*4+3]
					var bits int
					for dy := 0; dy < 4; dy++ {
						py := br*4 + dy
						if py >= 8 {
							continue
						}
						row := ld.letterPixelData[py]
						for dx := 0; dx < 2; dx++ {
							px := bc*2 + dx
							if px >= 6 || px >= len(row) {
								continue
							}
							if row[px] == 'X' {
								bit := brailleBit(dx, dy)
								bits |= bit
							}
						}
					}
					letterBrailleMIVIA[li][br][bc] = rune(0x2800 + bits)
				}
			}
		}
		// Pre-compute AGENT subtitle letters.
		for li, ld := range wordmarkSubLetters {
			for br := 0; br < 2; br++ {
				for bc := 0; bc < 3; bc++ {
					var bits int
					for dy := 0; dy < 4; dy++ {
						py := br*4 + dy
						if py >= 8 {
							continue
						}
						row := ld.letterPixelData[py]
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
					letterBrailleAGENT[li][br][bc] = rune(0x2800 + bits)
				}
			}
		}
	})
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

// renderWordmarkBraille renders the MIVIA + AGENT dot-matrix wordmark as braille.
// frame: animation frame index (for MIVIA glow wave); width: horizontal centering target.
// Returns a multi-line string (2 braille lines + 2 subtitle braille lines = 4 lines).
func renderWordmarkBraille(frame, width int) string {
	ensureBrailleWordmark()

	// ── MIVIA: two rows of braille with per-letter glow coloring ──
	miviaRows := [2]strings.Builder{}
	for li := 0; li < 5; li++ {
		b := letterBrightness(frame, li)
		col := brightnessColor(b)
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(col)).Bold(true)

		for br := 0; br < 2; br++ {
			for bc := 0; bc < 3; bc++ {
				ch := letterBrailleMIVIA[li][br][bc]
				miviaRows[br].WriteString(style.Render(string(ch)))
			}
			// Gap between letters (except after last)
			if li < 4 {
				miviaRows[br].WriteString(" ")
			}
		}
	}
	word := miviaRows[0].String() + "\n" + miviaRows[1].String()

	// ── AGENT: two rows of braille, static (no glow), light gray ──
	agentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Bold(true)
	agentRows := [2]strings.Builder{}
	for li := 0; li < 5; li++ {
		for br := 0; br < 2; br++ {
			for bc := 0; bc < 3; bc++ {
				ch := letterBrailleAGENT[li][br][bc]
				agentRows[br].WriteString(agentStyle.Render(string(ch)))
			}
			if li < 4 {
				agentRows[br].WriteString(" ")
			}
		}
	}
	sub := agentRows[0].String() + "\n" + agentRows[1].String()

	out := word + "\n" + sub

	if width > 0 {
		out = lipgloss.PlaceHorizontal(width, lipgloss.Center, out)
	}
	return out
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
