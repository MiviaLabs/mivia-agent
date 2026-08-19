package theme

import "testing"

// TestFirstPartySeparationPasses is the hard-fail CVD gate: every embedded
// first-party theme must meet its own documented CVDBudget.
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

func TestWorstCaseSeparationInvalidHex(t *testing.T) {
	colors := map[Role]string{
		RoleSuccess: "not-a-colour",
		RoleWarning: "#f3f34e",
		RoleDanger:  "#dc4e4e",
		RoleInfo:    "#5b8cff",
	}
	if _, _, err := WorstCaseSeparation(colors, StatusRoles()); err == nil {
		t.Fatal("expected error for invalid hex colour")
	}
}

func TestDichromatMatUnknownDichromacy(t *testing.T) {
	if _, err := dichromatMat(Dichromacy(99)); err == nil {
		t.Fatal("expected error for unknown dichromacy")
	}
}

func TestSimulateDichromatInvalidHex(t *testing.T) {
	if _, err := simulateDichromat("bad", Protanopia); err == nil {
		t.Fatal("expected error for invalid hex")
	}
	if _, _, _, err := simulateDichromatLinear("bad", Protanopia); err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func TestDE76InvalidHex(t *testing.T) {
	if _, err := dE76("bad", "#000000", Protanopia, true); err == nil {
		t.Fatal("expected error for invalid hexA under normal vision")
	}
	if _, err := dE76("#000000", "bad", Protanopia, true); err == nil {
		t.Fatal("expected error for invalid hexB under normal vision")
	}
	if _, err := dE76("bad", "#000000", Protanopia, false); err == nil {
		t.Fatal("expected error for invalid hexA under simulation")
	}
}

func TestMat3InverseSingular(t *testing.T) {
	singular := mat3{
		{1, 2, 3},
		{2, 4, 6}, // linearly dependent on row 0
		{0, 0, 1},
	}
	if _, err := singular.inverse(); err == nil {
		t.Fatal("expected error for singular matrix")
	}
}

func TestLabFromHexInvalid(t *testing.T) {
	if _, _, _, err := labFromHex("bad"); err == nil {
		t.Fatal("expected error for invalid hex")
	}
}
