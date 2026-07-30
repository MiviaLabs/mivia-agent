// Painter primitives for the state logo engine (logostate.go): the float
// brightness raster, dither/hash helpers, and the composable painters that
// draw the diamond — outline, facet shading, caustic sweep, edge lighting,
// vertex glints, grain. Geometry derives from canvas size so the same
// painters draw the hero and the mini mark.
package cli

import "math"

// logoGeom is the diamond geometry for a canvas, derived from its size so
// the same painters draw the hero and the mini mark.
type logoGeom struct{ cx, cy, r float64 }

func gridGeom(g *brightGrid) logoGeom {
	cx := float64(g.w-1) / 2
	cy := float64(g.h-1) / 2
	// Margin ratio matches the original hero (r=21 on a 48px canvas).
	r := math.Min(cx, cy) * (21.0 / 23.5)
	return logoGeom{cx: cx, cy: cy, r: r}
}

// shadeRamp is a 4-stop 256-color luminance ramp: dim, mid, bright, peak.
type shadeRamp [4]string

// stateAnim parameterizes one phase of the state language.
//
// paint draws the full-fidelity mark (dithered facet shading); paintMini is
// the small-canvas variant: at 8×8 px the dither reads as a pixel blob, so
// the mini mark is edge-lit — a clean outline whose edges carry the light.
type stateAnim struct {
	ramp      shadeRamp
	frozen    bool // frame 0 replicated — motion stopping is the signal
	paint     func(t float64, frame int, g *brightGrid, ge logoGeom)
	paintMini func(t float64, frame int, g *brightGrid, ge logoGeom)
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
func paintOutline(g *brightGrid, ge logoGeom, b float64) {
	paintLine(g, ge.cx, ge.cy-ge.r, ge.cx+ge.r, ge.cy, b)
	paintLine(g, ge.cx+ge.r, ge.cy, ge.cx, ge.cy+ge.r, b)
	paintLine(g, ge.cx, ge.cy+ge.r, ge.cx-ge.r, ge.cy, b)
	paintLine(g, ge.cx-ge.r, ge.cy, ge.cx, ge.cy-ge.r, b)
}

// paintFacets fills the four facets with dithered Lambert shading from one
// or more orbiting lights at angles phis. base is the unlit floor, gain the
// lit ceiling, expo the falloff sharpness.
func paintFacets(g *brightGrid, ge logoGeom, phis []float64, expo, base, gain float64) {
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			dx := (float64(x) - ge.cx) / ge.r
			dy := (float64(y) - ge.cy) / ge.r
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
func paintCaustic(g *brightGrid, ge logoGeom, pos float64) {
	c := -1.5 + math.Mod(pos, 1)*3.0
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			dx := (float64(x) - ge.cx) / ge.r
			dy := (float64(y) - ge.cy) / ge.r
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

// paintEdgesLit draws the diamond outline with per-edge Lambert lighting:
// each edge is a facet, lit by the orbiting light(s) at angles phis. This is
// the small-canvas renderer — crisp at sizes where dithering turns to mush.
func paintEdgesLit(g *brightGrid, ge logoGeom, phis []float64, expo, base, gain float64) {
	edges := [4][5]float64{
		{ge.cx, ge.cy - ge.r, ge.cx + ge.r, ge.cy, -math.Pi / 4},     // NE
		{ge.cx + ge.r, ge.cy, ge.cx, ge.cy + ge.r, math.Pi / 4},      // SE
		{ge.cx, ge.cy + ge.r, ge.cx - ge.r, ge.cy, 3 * math.Pi / 4},  // SW
		{ge.cx - ge.r, ge.cy, ge.cx, ge.cy - ge.r, -3 * math.Pi / 4}, // NW
	}
	for _, e := range edges {
		var l float64
		for _, p := range phis {
			if c := math.Cos(p - e[4]); c > l {
				l = c
			}
		}
		b := base + gain*math.Pow(l, expo)
		paintLine(g, e[0], e[1], e[2], e[3], b)
	}
}

// paintVertexGlints fires a star-cross flash at each vertex as the light
// angle t passes it — a discrete event, not ambience. Glint length scales
// with the mark so the mini diamond sparks stay in proportion.
func paintVertexGlints(g *brightGrid, ge logoGeom, t float64) {
	verts := [4][3]float64{
		{ge.cx, ge.cy - ge.r, -math.Pi / 2},
		{ge.cx + ge.r, ge.cy, 0},
		{ge.cx, ge.cy + ge.r, math.Pi / 2},
		{ge.cx - ge.r, ge.cy, math.Pi},
	}
	for _, v := range verts {
		s := math.Pow(math.Max(0, math.Cos(t-v[2])), 24)
		if s < 0.05 {
			continue
		}
		l := 1 + 0.24*ge.r*s
		paintLine(g, v[0]-l, v[1], v[0]+l, v[1], s)
		paintLine(g, v[0], v[1]-l, v[0], v[1]+l, s)
	}
}

// paintGrain adds sparse deterministic twinkle inside the mark. amt ∈ [0,1];
// held to half frame rate so it shimmers instead of strobing.
func paintGrain(g *brightGrid, ge logoGeom, frame int, amt float64) {
	if amt <= 0 {
		return
	}
	fs := frame / 2
	dens := 0.10 * amt
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			dx := (float64(x) - ge.cx) / ge.r
			dy := (float64(y) - ge.cy) / ge.r
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
