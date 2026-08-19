package theme

import "fmt"

// Dichromacy is a colour-vision deficiency simulated by the Vienot,
// Brettel and Mollon (1999) LMS model: sRGB -> linear -> LMS (via the
// Hunt-Pointer-Estevez matrix), project out the missing cone response in
// LMS space, convert back. research-panes.md section 3.
type Dichromacy int

const (
	Protanopia Dichromacy = iota
	Deuteranopia
	Tritanopia
)

// hpeMat is the Hunt-Pointer-Estevez matrix (linear sRGB -> LMS),
// with rows normalized so equal-energy white (and every neutral grey)
// maps to L=M=S. The dichromat projection matrices below assume this
// normalization (they were derived against a cone space where the
// neutral axis is L=M=S); the raw, unnormalized HPE matrix does not
// satisfy that on its own (its row sums are 1.027/0.9847/0.9182, not 1),
// which without normalization drifts a neutral grey's simulated colour
// noticeably off-axis. This is the forward matrix; the inverse used to
// go back to linear RGB is derived from it at call time
// (colormath.go: mat3.inverse) rather than hand-transcribed, so the pair
// can never silently drift apart.
var hpeMat = normalizeRows(mat3{
	{0.4002, 0.7076, -0.0808},
	{-0.2263, 1.1653, 0.0457},
	{0.0000, 0.0000, 0.9182},
})

// normalizeRows scales each row of m so its entries sum to 1.
func normalizeRows(m mat3) mat3 {
	var out mat3
	for i := 0; i < 3; i++ {
		sum := m[i][0] + m[i][1] + m[i][2]
		for j := 0; j < 3; j++ {
			out[i][j] = m[i][j] / sum
		}
	}
	return out
}

// Dichromat projection matrices in LMS space, Vienot/Brettel-derived: each
// zeroes out the missing cone's contribution by re-deriving it as a linear
// combination of the other two. The row that reconstructs the missing cone
// is normalized to sum to 1 (normalizeRows leaves the two identity rows
// unchanged, since [0,1,0] and [0,0,1] already sum to 1): with hpeMat also
// row-normalized, a neutral grey has L=M=S, and a row that sums to 1 maps
// it back to the same value, so the neutral axis is exactly preserved.
// Without this, the commonly-published raw coefficients (calibrated for a
// differently-scaled LMS space) visibly shift grey off-axis under
// simulation, which is what this package's TestSimulateDichromatIsStable
// guards against.
var (
	protanopiaMat = normalizeRows(mat3{
		{0, 2.02344, -2.52581},
		{0, 1, 0},
		{0, 0, 1},
	})
	deuteranopiaMat = normalizeRows(mat3{
		{1, 0, 0},
		{0.494207, 0, 1.24827},
		{0, 0, 1},
	})
	tritanopiaMat = normalizeRows(mat3{
		{1, 0, 0},
		{0, 1, 0},
		{-0.395913, 0.801109, 0},
	})
)

func dichromatMat(d Dichromacy) (mat3, error) {
	switch d {
	case Protanopia:
		return protanopiaMat, nil
	case Deuteranopia:
		return deuteranopiaMat, nil
	case Tritanopia:
		return tritanopiaMat, nil
	default:
		return mat3{}, fmt.Errorf("theme: unknown dichromacy %d", d)
	}
}

// simulateDichromatLinear returns the unclamped linear-RGB triplet a
// viewer with the given dichromacy perceives in place of hex. Values may
// fall outside [0,1]: the simulation can push a colour out of the sRGB
// gamut, and clamping here (as opposed to only at final display time)
// would collapse genuinely different colours to the same clamped value
// and report a false dE-0 collision.
func simulateDichromatLinear(hex string, d Dichromacy) (r, g, b float64, err error) {
	c, err := parseHex(hex)
	if err != nil {
		return 0, 0, 0, err
	}
	lr, lg, lb := c.linear()

	lms := hpeMat.mulVec([3]float64{lr, lg, lb})

	proj, err := dichromatMat(d)
	if err != nil {
		return 0, 0, 0, err
	}
	simLMS := proj.mulVec(lms)

	inv, err := hpeMat.inverse()
	if err != nil {
		return 0, 0, 0, err
	}
	simLinear := inv.mulVec(simLMS)
	return simLinear[0], simLinear[1], simLinear[2], nil
}

// simulateDichromat returns the hex colour a viewer with the given
// dichromacy perceives in place of hex, clamped to sRGB for display. Do
// not use this for dE measurement (see simulateDichromatLinear).
func simulateDichromat(hex string, d Dichromacy) (string, error) {
	r, g, b, err := simulateDichromatLinear(hex, d)
	if err != nil {
		return "", err
	}
	return linearToSRGBColor(r, g, b).hex(), nil
}

// labFromLinearRGB converts unclamped linear RGB directly to CIELAB,
// without an intermediate hex round-trip.
func labFromLinearRGB(r, g, b float64) (l, a, bb float64) {
	xyz := rgbToXYZMat.mulVec([3]float64{r, g, b})
	return xyzToLab(xyz[0], xyz[1], xyz[2])
}

// dE76 computes the CIE76 dE between two hex colours as perceived under
// the given dichromacy (Protanopia/Deuteranopia/Tritanopia simulate
// first; pass normal=true for normal vision, ignoring d).
func dE76(hexA, hexB string, d Dichromacy, normal bool) (float64, error) {
	labOf := func(hex string) (l, a, b float64, err error) {
		if normal {
			return labFromHex(hex)
		}
		r, g, b2, err := simulateDichromatLinear(hex, d)
		if err != nil {
			return 0, 0, 0, err
		}
		l, a, b = labFromLinearRGB(r, g, b2)
		return l, a, b, nil
	}
	l1, a1, b1, err := labOf(hexA)
	if err != nil {
		return 0, err
	}
	l2, a2, b2, err := labOf(hexB)
	if err != nil {
		return 0, err
	}
	return cie76(l1, a1, b1, l2, a2, b2), nil
}

// SeparationPair is one status-role pair's worst measured separation
// across normal vision and all three dichromacies.
type SeparationPair struct {
	A, B       Role
	WorstDE    float64
	WorstUnder string // "normal", "protanopia", "deuteranopia", or "tritanopia"
}

// WorstCaseSeparation measures every pair of the given status roles under
// normal vision and all three dichromacies, and returns the worst
// (smallest) dE found, plus the per-pair detail. research-panes.md
// section 3: the set that must stay separable is
// {success, warning, danger, info}; accent is chrome and is exempt.
func WorstCaseSeparation(colors map[Role]string, roles []Role) (worst float64, detail []SeparationPair, err error) {
	worst = -1
	for i := 0; i < len(roles); i++ {
		for j := i + 1; j < len(roles); j++ {
			a, b := roles[i], roles[j]
			hexA, okA := colors[a]
			hexB, okB := colors[b]
			if !okA || !okB {
				return 0, nil, fmt.Errorf("theme: missing colour for role pair %s/%s", a, b)
			}
			pair := SeparationPair{A: a, B: b, WorstDE: -1}
			checks := []struct {
				normal bool
				d      Dichromacy
				name   string
			}{
				{true, 0, "normal"},
				{false, Protanopia, "protanopia"},
				{false, Deuteranopia, "deuteranopia"},
				{false, Tritanopia, "tritanopia"},
			}
			for _, chk := range checks {
				de, err := dE76(hexA, hexB, chk.d, chk.normal)
				if err != nil {
					return 0, nil, err
				}
				if pair.WorstDE < 0 || de < pair.WorstDE {
					pair.WorstDE = de
					pair.WorstUnder = chk.name
				}
			}
			detail = append(detail, pair)
			if worst < 0 || pair.WorstDE < worst {
				worst = pair.WorstDE
			}
		}
	}
	if worst < 0 {
		worst = 0
	}
	return worst, detail, nil
}

// HardFailSeparation reports whether a theme's worst-case status
// separation meets its own documented CVDBudget. This is the build-time
// gate: hard-fail for first-party themes, informational for third-party
// (research-panes.md section 3.1: no third-party palette in the survey
// was CVD-clean, so a hard gate there would reject nearly every upstream
// scheme).
func HardFailSeparation(t Theme) (worst float64, detail []SeparationPair, ok bool, err error) {
	worst, detail, err = WorstCaseSeparation(t.Colors, StatusRoles())
	if err != nil {
		return 0, nil, false, err
	}
	return worst, detail, worst >= t.CVDBudget, nil
}
