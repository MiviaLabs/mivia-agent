// State logo engine: the brand diamond as an agent-state language.
//
// One geometry, one renderer; each brand phase is a parameter set over the
// same braille canvas. Motion carries the meaning:
//
//	idle      — one light, dwell-and-snap around the facets (clockwork)
//	thinking  — the light orbits faster, a trace of grain
//	streaming — a specular band flows through the glass (caustic)
//	tools     — dim facets, a glint fires at each vertex (events)
//	multi     — two counter-rotating lights, one per agent
//	error     — motion stops, the light locks (frozen frame)
//
// Fidelity: geometry resolves at 2×4 braille subpixels per cell; color
// resolves per cell via a 4-stop 256-color ramp per phase. Luminance below
// cell resolution is carried by ordered (Bayer) dithering of the dots.
// Every painter is periodic in t ∈ [0, 2π), so frames precompute once and
// loop without a seam. Grain is hashed from the frame index — deterministic,
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
	stateLogoPxW     = 48 // 24 braille columns
	stateLogoPxH     = 48 // 12 braille rows
	stateLogoNFrames = 48 // 48 × 80ms tick ≈ 3.8s loop
)

// Diamond geometry in pixel space (shared by every painter).
const (
	stateLogoCX = float64(stateLogoPxW-1) / 2
	stateLogoCY = float64(stateLogoPxH-1) / 2
	stateLogoR  = 21
)

// shadeRamp is a 4-stop 256-color luminance ramp: dim, mid, bright, peak.
type shadeRamp [4]string

// stateAnim parameterizes one phase of the state language.
type stateAnim struct {
	ramp   shadeRamp
	frozen bool // frame 0 replicated — motion stopping is the signal
	paint  func(t float64, frame int, g *brightGrid)
}

// brightGrid is a float raster: 0 = dark, (0,1] = lit with that luminance.
// Overlapping painters max-blend, so layers compose without ordering rules.
type brightGrid struct {
	w, h int
	v    []float64
}

func newBrightGrid(w, h int) *brightGrid {
	return &brightGrid{w: w, h: h, v: make([]float64, w*h)}
}

func (g *brightGrid) lit(x, y int, b float64) {
	if x < 0 || y < 0 || x >= g.w || y >= g.h {
		return
	}
	if i := y*g.w + x; b > g.v[i] {
		g.v[i] = b
	}
}

func (g *brightGrid) litF(x, y, b float64) {
	g.lit(int(math.Round(x)), int(math.Round(y)), b)
}

// bayer4 is the 4×4 ordered-dither threshold matrix.
var bayer4 = [4][4]float64{
	{0, 8, 2, 10},
	{12, 4, 14, 6},
	{3, 11, 1, 9},
	{15, 7, 13, 5},
}

func bayerAt(x, y int) float64 {
	return (bayer4[y%4][x%4] + 0.5) / 16
}

// grainHash is a deterministic per-(pixel, frame) value in [0,1).
func grainHash(x, y, f int) float64 {
	n := uint32(x)*374761393 + uint32(y)*668265263 + uint32(f)*2246822519
	n ^= n >> 13
	n *= 1274126177
	n ^= n >> 16
	return float64(n) / float64(math.MaxUint32)
}

// ─── Painter primitives ───────────────────────────────────────────────

func paintLine(g *brightGrid, x0, y0, x1, y1, b float64) {
	steps := int(math.Ceil(math.Max(math.Abs(x1-x0), math.Abs(y1-y0))))*2 + 1
	for i := 0; i <= steps; i++ {
		f := float64(i) / float64(steps)
		g.litF(x0+(x1-x0)*f, y0+(y1-y0)*f, b)
	}
}

// paintOutline draws the four diamond edges at full luminance — the mark
// itself never dims or disappears, whatever the interior does.
func paintOutline(g *brightGrid, b float64) {
	cx, cy, r := stateLogoCX, stateLogoCY, float64(stateLogoR)
	paintLine(g, cx, cy-r, cx+r, cy, b)
	paintLine(g, cx+r, cy, cx, cy+r, b)
	paintLine(g, cx, cy+r, cx-r, cy, b)
	paintLine(g, cx-r, cy, cx, cy-r, b)
}

// paintFacets fills the four facets with dithered Lambert shading from one
// or more orbiting lights at angles phis. base is the unlit floor, gain the
// lit ceiling, expo the falloff sharpness.
func paintFacets(g *brightGrid, phis []float64, expo, base, gain float64) {
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			dx := (float64(x) - stateLogoCX) / stateLogoR
			dy := (float64(y) - stateLogoCY) / stateLogoR
			if math.Abs(dx)+math.Abs(dy) > 0.94 {
				continue
			}
			// Facet normal: one of the four diagonal directions.
			var fa float64
			switch {
			case dx >= 0 && dy < 0:
				fa = -math.Pi / 4
			case dx >= 0:
				fa = math.Pi / 4
			case dy < 0:
				fa = -3 * math.Pi / 4
			default:
				fa = 3 * math.Pi / 4
			}
			var l float64
			for _, p := range phis {
				if c := math.Cos(p - fa); c > l {
					l = c
				}
			}
			b := base + gain*math.Pow(l, expo)
			if b > bayerAt(x, y)*0.95 {
				g.lit(x, y, b*0.92)
			}
		}
	}
}

// paintCaustic sweeps a narrow specular band diagonally through the glass.
// pos ∈ [0,1) travels off-screen to off-screen, so the loop wrap is invisible.
func paintCaustic(g *brightGrid, pos float64) {
	c := -1.5 + math.Mod(pos, 1)*3.0
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			dx := (float64(x) - stateLogoCX) / stateLogoR
			dy := (float64(y) - stateLogoCY) / stateLogoR
			if math.Abs(dx)+math.Abs(dy) > 0.94 {
				continue
			}
			u := (dx - dy) / 2
			d := (u - c) / 0.16
			b := 0.12 + 0.9*math.Exp(-d*d)
			if b > bayerAt(x, y)*0.95 {
				g.lit(x, y, b*0.94)
			}
		}
	}
}

// paintVertexGlints fires a star-cross flash at each vertex as the light
// angle t passes it — a discrete event, not ambience.
func paintVertexGlints(g *brightGrid, t float64) {
	cx, cy, r := stateLogoCX, stateLogoCY, float64(stateLogoR)
	verts := [4][3]float64{
		{cx, cy - r, -math.Pi / 2},
		{cx + r, cy, 0},
		{cx, cy + r, math.Pi / 2},
		{cx - r, cy, math.Pi},
	}
	for _, v := range verts {
		s := math.Pow(math.Max(0, math.Cos(t-v[2])), 24)
		if s < 0.05 {
			continue
		}
		l := 1 + 5*s
		paintLine(g, v[0]-l, v[1], v[0]+l, v[1], s)
		paintLine(g, v[0], v[1]-l, v[0], v[1]+l, s)
	}
}

// paintGrain adds sparse deterministic twinkle inside the mark. amt ∈ [0,1];
// held to half frame rate so it shimmers instead of strobing.
func paintGrain(g *brightGrid, frame int, amt float64) {
	if amt <= 0 {
		return
	}
	fs := frame / 2
	dens := 0.10 * amt
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			dx := (float64(x) - stateLogoCX) / stateLogoR
			dy := (float64(y) - stateLogoCY) / stateLogoR
			if math.Abs(dx)+math.Abs(dy) > 0.92 {
				continue
			}
			r := grainHash(x, y, fs)
			switch {
			case r < dens*0.06:
				g.lit(x, y, 0.9)
			case r < dens:
				g.lit(x, y, 0.18+0.3*grainHash(y, x, fs))
			}
		}
	}
}

// clockPhase eases the orbit into dwell-and-snap: the light lingers on each
// facet, then sweeps to the next. Periodic: clockPhase(t+2π) = clockPhase(t)+2π.
func clockPhase(t float64) float64 {
	return t - 0.22*math.Sin(4*t)
}

// ─── Phase parameter sets ─────────────────────────────────────────────

var stateAnims = map[brandPhase]stateAnim{
	phaseIdle: {
		ramp: shadeRamp{"238", "245", "251", "15"},
		paint: func(t float64, _ int, g *brightGrid) {
			paintFacets(g, []float64{clockPhase(t)}, 3, 0.05, 0.60)
			paintOutline(g, 1)
		},
	},
	phaseThinking: {
		ramp: shadeRamp{"23", "30", "44", "51"},
		paint: func(t float64, frame int, g *brightGrid) {
			paintFacets(g, []float64{2 * t}, 2, 0.07, 0.85)
			paintGrain(g, frame, 0.12)
			paintOutline(g, 1)
		},
	},
	phaseStreaming: {
		ramp: shadeRamp{"24", "31", "33", "75"},
		paint: func(t float64, _ int, g *brightGrid) {
			paintCaustic(g, t/(2*math.Pi)*2)
			paintOutline(g, 1)
		},
	},
	phaseTools: {
		ramp: shadeRamp{"58", "136", "178", "220"},
		paint: func(t float64, _ int, g *brightGrid) {
			paintFacets(g, []float64{t}, 2, 0.05, 0.45)
			paintVertexGlints(g, t)
			paintOutline(g, 1)
		},
	},
	phaseMulti: {
		ramp: shadeRamp{"53", "96", "170", "213"},
		paint: func(t float64, _ int, g *brightGrid) {
			paintFacets(g, []float64{t, -t}, 1.8, 0.06, 0.72)
			paintOutline(g, 1)
		},
	},
	phaseQueued: {
		ramp: shadeRamp{"22", "28", "40", "82"},
		paint: func(t float64, _ int, g *brightGrid) {
			paintFacets(g, []float64{clockPhase(t)}, 3, 0.05, 0.60)
			paintOutline(g, 1)
		},
	},
	phaseError: {
		ramp:   shadeRamp{"52", "88", "160", "196"},
		frozen: true,
		paint: func(_ float64, _ int, g *brightGrid) {
			paintFacets(g, []float64{-math.Pi / 2}, 3, 0.05, 0.70)
			paintOutline(g, 1)
		},
	},
	phaseCancel: {
		ramp:   shadeRamp{"235", "238", "242", "245"},
		frozen: true,
		paint: func(_ float64, _ int, g *brightGrid) {
			paintOutline(g, 1)
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

var stateLogoCache = struct {
	mu     sync.Mutex
	frames map[brandPhase][]string
}{frames: map[brandPhase][]string{}}

// stateLogoFrames returns the precomputed seamless loop for a phase.
func stateLogoFrames(phase brandPhase) []string {
	key := logoStateKey(phase)
	stateLogoCache.mu.Lock()
	defer stateLogoCache.mu.Unlock()
	if f, ok := stateLogoCache.frames[key]; ok {
		return f
	}
	anim := stateAnims[key]
	frames := make([]string, stateLogoNFrames)
	if anim.frozen {
		g := newBrightGrid(stateLogoPxW, stateLogoPxH)
		anim.paint(0, 0, g)
		still := g.renderBrailleShaded(anim.ramp)
		for i := range frames {
			frames[i] = still
		}
	} else {
		for i := range frames {
			t := float64(i) / stateLogoNFrames * 2 * math.Pi
			g := newBrightGrid(stateLogoPxW, stateLogoPxH)
			anim.paint(t, i, g)
			frames[i] = g.renderBrailleShaded(anim.ramp)
		}
	}
	stateLogoCache.frames[key] = frames
	return frames
}

// logoMotionDisabled honors the reduced-motion escape hatch.
func logoMotionDisabled() bool {
	return os.Getenv("MIVIA_NO_MOTION") != ""
}

// renderStateLogo renders one frame of a phase's loop, optionally centered.
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
