package theme

import "testing"

// TestFirstPartySeparationPasses is the hard-fail CVD gate: every embedded
// first-party theme must meet its own documented CVDBudget over the pairs
// this package's simulator is fit to arbitrate (known_gaps.go).
func TestFirstPartySeparationPasses(t *testing.T) {
	themes, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if !th.FirstParty {
			continue
		}
		t.Run(th.Name, func(t *testing.T) {
			worst, detail, ok, err := HardFailSeparation(th)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Errorf("%s: worst-case dE %.2f below budget %.2f; detail: %+v", th.Name, worst, th.CVDBudget, detail)
			}
		})
	}
}

func TestSimulateDichromatIsStable(t *testing.T) {
	// A neutral grey should stay very close to itself under every
	// dichromacy: dichromat models simulate hue-channel loss, not
	// luminance loss, so an achromatic colour is the fixed point.
	for _, d := range []Dichromacy{Protanopia, Deuteranopia, Tritanopia} {
		got, err := simulateDichromat("#808080", d)
		if err != nil {
			t.Fatal(err)
		}
		de, err := dE76("#808080", got, d, true) // compare in normal-vision Lab, no re-simulation
		if err != nil {
			t.Fatal(err)
		}
		if de > 2.0 {
			t.Errorf("dichromacy %d: grey drifted too far, dE=%.2f (got %s)", d, de, got)
		}
	}
}

func TestWorstCaseSeparationMissingRole(t *testing.T) {
	_, _, err := WorstCaseSeparation(map[Role]string{RoleSuccess: "#4edc4e"}, StatusRoles())
	if err == nil {
		t.Fatal("expected error for missing role colour")
	}
}
