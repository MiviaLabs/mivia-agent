package theme

import "testing"

// TestFirstPartyContrastPasses is the hard-fail contrast gate: every
// embedded first-party theme must clear every check in AllContrastChecks
// with zero exceptions.
func TestFirstPartyContrastPasses(t *testing.T) {
	themes, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if !th.FirstParty {
			continue
		}
		t.Run(th.Name, func(t *testing.T) {
			fails, err := ValidateContrast(th)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range fails {
				t.Errorf("%s", f)
			}
		})
	}
}

func TestValidateContrastMissingRole(t *testing.T) {
	th := Theme{Name: "broken", Colors: map[Role]string{}}
	if _, err := ValidateContrast(th); err == nil {
		t.Fatal("expected error for theme missing role colours")
	}
}

func TestContrastRatioKnownValues(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"#ffffff", "#000000", 21.0},
		{"#767676", "#ffffff", 4.54}, // WCAG's own published reference pair
	}
	for _, c := range cases {
		got, err := contrastRatio(c.a, c.b)
		if err != nil {
			t.Fatal(err)
		}
		if diff := got - c.want; diff > 0.02 || diff < -0.02 {
			t.Errorf("contrastRatio(%s, %s) = %.3f, want ~%.2f", c.a, c.b, got, c.want)
		}
	}
}
