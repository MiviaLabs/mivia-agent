package theme

import (
	"fmt"
	"math/rand"
)

// hueWindow constrains a searched status colour to a conventional hue
// range: research-panes.md section 3.2 found that an unconstrained search
// scores higher on separation but picks unconventional hues (cyan
// success, violet info) that a reader would not recognise as the
// intended status. Degrees, [0,360).
type hueWindow struct{ lo, hi float64 }

// statusHueWindows: green/amber/red/blue, the conventional mapping.
var statusHueWindows = map[Role]hueWindow{
	RoleSuccess: {90, 150},
	RoleWarning: {40, 70},
	RoleDanger:  {350, 375}, // wraps past 360; sampled mod 360
	RoleInfo:    {210, 250},
}

// SearchOptions configures SearchStatusPalette.
type SearchOptions struct {
	// MinContrast is the WCAG contrast ratio each candidate colour must
	// meet against bg (config.WCAGAALarge is the conventional floor for
	// a status word).
	MinContrast float64
	// Iterations bounds how many full {success,warning,danger,info}
	// candidate sets are tried. More iterations trade search time for a
	// better worst-case separation.
	Iterations int
	// Seed makes the search reproducible: same bg + options + seed
	// always finds the same palette.
	Seed int64
}

// SearchResult is one candidate palette and its measured worst-case
// separation.
type SearchResult struct {
	Colors  map[Role]string
	WorstDE float64
	Detail  []SeparationPair
}

// SearchStatusPalette searches hue/saturation/lightness space for a
// {success, warning, danger, info} set that meets MinContrast against bg
// and maximises worst-case CVD separation (WorstCaseSeparation), within
// the conventional hue windows in statusHueWindows.
//
// research-panes.md section 3.2: hand-picking a status palette lost to
// this kind of search by 4x on worst-case separation. This function is
// for generating a *new* first-party or user theme against the
// constraint; it never rewrites an existing theme's shipped colours.
func SearchStatusPalette(bg string, opts SearchOptions) (SearchResult, error) {
	if opts.Iterations <= 0 {
		return SearchResult{}, fmt.Errorf("theme: SearchOptions.Iterations must be > 0")
	}
	if _, err := parseHex(bg); err != nil {
		return SearchResult{}, err
	}

	rng := rand.New(rand.NewSource(opts.Seed))
	roles := []Role{RoleSuccess, RoleWarning, RoleDanger, RoleInfo}

	sampleOne := func(r Role) (string, bool) {
		win := statusHueWindows[r]
		for attempt := 0; attempt < 64; attempt++ {
			h := win.lo + rng.Float64()*(win.hi-win.lo)
			for h >= 360 {
				h -= 360
			}
			s := 0.55 + rng.Float64()*0.45 // 0.55-1.0: saturated, per research-panes.md 3.1
			l := 0.35 + rng.Float64()*0.35 // 0.35-0.70: keep away from near-black/near-white
			hex := hslToSRGB(h, s, l).hex()
			ratio, err := contrastRatio(hex, bg)
			if err == nil && ratio >= opts.MinContrast {
				return hex, true
			}
		}
		return "", false
	}

	var best SearchResult
	for i := 0; i < opts.Iterations; i++ {
		candidate := make(map[Role]string, 4)
		ok := true
		for _, r := range roles {
			hex, found := sampleOne(r)
			if !found {
				ok = false
				break
			}
			candidate[r] = hex
		}
		if !ok {
			continue
		}
		worst, detail, err := WorstCaseSeparation(candidate, roles)
		if err != nil {
			return SearchResult{}, err
		}
		if worst > best.WorstDE {
			best = SearchResult{Colors: candidate, WorstDE: worst, Detail: detail}
		}
	}
	if best.Colors == nil {
		return SearchResult{}, fmt.Errorf("theme: search found no candidate meeting contrast %.1f against %s in %d iterations", opts.MinContrast, bg, opts.Iterations)
	}
	return best, nil
}
