// State logo engine: the brand diamond as an agent-state language.
//
// One geometry, one renderer; each brand phase is a parameter set over the
// same braille canvas. Motion carries the meaning:
//
//	idle      - one light, dwell-and-snap around the facets (clockwork)
//	thinking  - the light orbits faster, a trace of grain
//	streaming - a specular band flows through the glass (caustic)
//	tools     - dim facets, a glint fires at each vertex (events)
//	multi     - two counter-rotating lights, one per agent
//	error     - motion stops, the light locks (frozen frame)
//
// The engine renders at two sizes from the same painters: the 48×48 hero
// (splash) and the 8×8 mini mark that leads the two-line chat status bar -
// the diamond is always on screen, whatever the phase.
//
// Fidelity: geometry resolves at 2×4 braille subpixels per cell; color
// resolves per cell via a 4-stop 256-color ramp per phase. Luminance below
// cell resolution is carried by ordered (Bayer) dithering of the dots.
// Every painter is periodic in t ∈ [0, 2π), so frames precompute once and
// loop without a seam. Grain is hashed from the frame index - deterministic,
// loopable, never math/rand.
package cli

import (
	"math"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

const (
	stateLogoPxW     = 48 // hero: 24 braille columns
	stateLogoPxH     = 48 // hero: 12 braille rows
	miniLogoPxW      = 8  // mini: 4 braille columns
	miniLogoPxH      = 8  // mini: 2 braille rows
	stateLogoNFrames = 48 // 48 × 80ms tick ≈ 3.8s loop
)

// ─── Phase parameter sets ─────────────────────────────────────────────

var stateAnims = map[brandPhase]stateAnim{
	phaseIdle: {
		ramp: shadeRamp{"238", "245", "251", "15"},
		paint: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintFacets(g, ge, []float64{clockPhase(t)}, 3, 0.05, 0.60)
			paintOutline(g, ge, 1)
		},
		paintMini: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintEdgesLit(g, ge, []float64{clockPhase(t)}, 3, 0.30, 0.70)
		},
	},
	phaseThinking: {
		ramp: shadeRamp{"23", "30", "44", "51"},
		paint: func(t float64, frame int, g *brightGrid, ge logoGeom) {
			paintFacets(g, ge, []float64{2 * t}, 2, 0.07, 0.85)
			paintGrain(g, ge, frame, 0.12)
			paintOutline(g, ge, 1)
		},
		paintMini: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintEdgesLit(g, ge, []float64{2 * t}, 2, 0.30, 0.70)
		},
	},
	phaseStreaming: {
		ramp: shadeRamp{"24", "31", "33", "75"},
		paint: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintCaustic(g, ge, t/(2*math.Pi)*2)
			paintOutline(g, ge, 1)
		},
		paintMini: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintEdgesLit(g, ge, []float64{-2 * t}, 2, 0.30, 0.70)
		},
	},
	phaseTools: {
		ramp: shadeRamp{"58", "136", "178", "220"},
		paint: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintFacets(g, ge, []float64{t}, 2, 0.05, 0.45)
			paintVertexGlints(g, ge, t)
			paintOutline(g, ge, 1)
		},
		paintMini: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintEdgesLit(g, ge, []float64{t}, 2, 0.25, 0.40)
			paintVertexGlints(g, ge, t)
		},
	},
	phaseMulti: {
		ramp: shadeRamp{"53", "96", "170", "213"},
		paint: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintFacets(g, ge, []float64{t, -t}, 1.8, 0.06, 0.72)
			paintOutline(g, ge, 1)
		},
		paintMini: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintEdgesLit(g, ge, []float64{t, -t}, 1.8, 0.28, 0.62)
		},
	},
	phaseQueued: {
		ramp: shadeRamp{"22", "28", "40", "82"},
		paint: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintFacets(g, ge, []float64{clockPhase(t)}, 3, 0.05, 0.60)
			paintOutline(g, ge, 1)
		},
		paintMini: func(t float64, _ int, g *brightGrid, ge logoGeom) {
			paintEdgesLit(g, ge, []float64{clockPhase(t)}, 3, 0.30, 0.70)
		},
	},
	phaseError: {
		ramp:   shadeRamp{"52", "88", "160", "196"},
		frozen: true,
		paint: func(_ float64, _ int, g *brightGrid, ge logoGeom) {
			paintFacets(g, ge, []float64{-math.Pi / 2}, 3, 0.05, 0.70)
			paintOutline(g, ge, 1)
		},
		paintMini: func(_ float64, _ int, g *brightGrid, ge logoGeom) {
			paintEdgesLit(g, ge, []float64{-math.Pi / 2}, 3, 0.30, 0.70)
		},
	},
	phaseCancel: {
		ramp:   shadeRamp{"235", "238", "242", "245"},
		frozen: true,
		paint: func(_ float64, _ int, g *brightGrid, ge logoGeom) {
			paintOutline(g, ge, 1)
		},
		paintMini: func(_ float64, _ int, g *brightGrid, ge logoGeom) {
			paintOutline(g, ge, 0.5)
		},
	},
}

// logoStateKey collapses aliases to the phase that owns the animation.
func logoStateKey(p brandPhase) brandPhase {
	switch p {
	case phaseWelcome:
		return phaseIdle
	case phaseAwaiting:
		return phaseThinking
	}
	if _, ok := stateAnims[p]; !ok {
		return phaseIdle
	}
	return p
}

// ─── Shaded braille rendering ─────────────────────────────────────────

// rampBucket maps luminance to a ramp stop; the top stop renders bold.
func rampBucket(b float64) int {
	switch {
	case b < 0.30:
		return 0
	case b < 0.55:
		return 1
	case b < 0.80:
		return 2
	default:
		return 3
	}
}

// renderBrailleShaded packs the grid into braille with per-cell ramp colors.
// Consecutive same-color cells share one style run to keep frames compact.
func (g *brightGrid) renderBrailleShaded(ramp shadeRamp) string {
	styles := [4]lipgloss.Style{}
	for i, c := range ramp {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(c))
		if i == 3 {
			s = s.Bold(true)
		}
		styles[i] = s
	}
	cols, rows := g.w/2, g.h/4
	var out strings.Builder
	var run strings.Builder
	for row := 0; row < rows; row++ {
		if row > 0 {
			out.WriteByte('\n')
		}
		runBucket := -1 // -1 = blank run
		flush := func() {
			if run.Len() == 0 {
				return
			}
			if runBucket < 0 {
				out.WriteString(run.String())
			} else {
				out.WriteString(styles[runBucket].Render(run.String()))
			}
			run.Reset()
		}
		for col := 0; col < cols; col++ {
			var bits int
			var peak float64
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					b := g.v[(row*4+dy)*g.w+col*2+dx]
					if b > 0.03 {
						bits |= brailleBits[dy][dx]
						if b > peak {
							peak = b
						}
					}
				}
			}
			bucket := -1
			if bits != 0 {
				bucket = rampBucket(peak)
			}
			if bucket != runBucket {
				flush()
				runBucket = bucket
			}
			run.WriteRune(rune(0x2800 + bits))
		}
		flush()
	}
	return out.String()
}

// ─── Frame cache + public surface ─────────────────────────────────────

type stateLogoSizeKey struct {
	phase brandPhase
	w, h  int
}

var stateLogoCache = struct {
	mu     sync.Mutex
	frames map[stateLogoSizeKey][]string
}{frames: map[stateLogoSizeKey][]string{}}

// stateLogoFramesSized returns the precomputed seamless loop for a phase at
// a canvas size (pixels; braille compresses 2×4 per cell).
func stateLogoFramesSized(phase brandPhase, pxW, pxH int) []string {
	key := stateLogoSizeKey{phase: logoStateKey(phase), w: pxW, h: pxH}
	stateLogoCache.mu.Lock()
	defer stateLogoCache.mu.Unlock()
	if f, ok := stateLogoCache.frames[key]; ok {
		return f
	}
	anim := stateAnims[key.phase]
	paint := anim.paint
	// Small canvases switch to the edge-lit painter: dithered facet shading
	// below hero scale reads as a pixel blob, not a diamond.
	if pxH <= miniLogoPxH && anim.paintMini != nil {
		paint = anim.paintMini
	}
	frames := make([]string, stateLogoNFrames)
	if anim.frozen {
		g := newBrightGrid(pxW, pxH)
		paint(0, 0, g, gridGeom(g))
		still := g.renderBrailleShaded(anim.ramp)
		for i := range frames {
			frames[i] = still
		}
	} else {
		for i := range frames {
			t := float64(i) / stateLogoNFrames * 2 * math.Pi
			g := newBrightGrid(pxW, pxH)
			paint(t, i, g, gridGeom(g))
			frames[i] = g.renderBrailleShaded(anim.ramp)
		}
	}
	stateLogoCache.frames[key] = frames
	return frames
}

// renderStateLogoRows returns one frame's rows at an arbitrary canvas size.
func renderStateLogoRows(phase brandPhase, frame, pxW, pxH int) []string {
	frames := stateLogoFramesSized(phase, pxW, pxH)
	if logoMotionDisabled() {
		frame = 0
	}
	if frame < 0 {
		frame = -frame
	}
	return strings.Split(frames[frame%len(frames)], "\n")
}

// stateLogoFrames is the hero-size loop (welcome splash).
func stateLogoFrames(phase brandPhase) []string {
	return stateLogoFramesSized(phase, stateLogoPxW, stateLogoPxH)
}

// logoMotionDisabled honors the reduced-motion escape hatch.
func logoMotionDisabled() bool {
	return os.Getenv(envNoMotion) != ""
}

// renderStateLogo renders one hero frame of a phase's loop, optionally centered.
func renderStateLogo(phase brandPhase, frame, width int) string {
	frames := stateLogoFrames(phase)
	if logoMotionDisabled() {
		frame = 0
	}
	if frame < 0 {
		frame = -frame
	}
	art := frames[frame%len(frames)]
	if width > 0 {
		art = lipgloss.PlaceHorizontal(width, lipgloss.Center, art)
	}
	return art
}

// stateLogoMiniRows renders the 2-row mini mark for the status header.
// Both rows are 4 cells wide; the diamond is present in every phase.
func stateLogoMiniRows(phase brandPhase, frame int) [2]string {
	frames := stateLogoFramesSized(phase, miniLogoPxW, miniLogoPxH)
	if logoMotionDisabled() {
		frame = 0
	}
	if frame < 0 {
		frame = -frame
	}
	art := frames[frame%len(frames)]
	parts := strings.SplitN(art, "\n", 2)
	if len(parts) < 2 {
		return [2]string{art, strings.Repeat("⠀", miniLogoPxW/2)}
	}
	return [2]string{parts[0], parts[1]}
}
