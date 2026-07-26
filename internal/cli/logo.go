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
		Foreground(lipgloss.Color("15")).
		Bold(true).
		Render("MIVIA")
	sub := lipgloss.NewStyle().
		Foreground(lipgloss.Color("8")).
		Render("agent")
	line := word + "  " + sub
	if width > 0 {
		return lipgloss.PlaceHorizontal(width, lipgloss.Center, line)
	}
	return line
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
