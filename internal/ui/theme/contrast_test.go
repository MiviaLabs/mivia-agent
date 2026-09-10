package theme

import (
	"strings"
	"testing"
)

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

func TestValidateContrastMissingBGOnly(t *testing.T) {
	// FG present, BG absent: exercises the second (BG) missing-role branch
	// distinctly from the first (FG) one above.
	colors := map[Role]string{}
	for _, r := range AllRoles() {
		colors[r] = "#808080"
	}
	delete(colors, RoleBG)
	th := Theme{Name: "broken-bg", Colors: colors}
	if _, err := ValidateContrast(th); err == nil {
		t.Fatal("expected error for theme missing bg colour")
	}
}

func TestContrastFailureString(t *testing.T) {
	f := ContrastFailure{
		Check: ContrastCheck{FG: RoleFG, BG: RoleBG, Min: 4.5, Label: "body text"},
		Ratio: 2.1,
	}
	got := f.String()
	for _, want := range []string{"fg", "bg", "2.10", "4.5", "body text"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
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

// TestContrastChecksCoverInsetFill pins the two inset-fill rows: prose
// and inline code draw RoleFG and RoleFGSubtle on RoleBGInset (chat TUI
// polish plan, Phase 0), so those pairs must be in the gate table.
func TestContrastChecksCoverInsetFill(t *testing.T) {
	want := map[[2]Role]float64{
		{RoleFG, RoleBGInset}:       4.5,
		{RoleFGSubtle, RoleBGInset}: 3.0,
	}
	for _, chk := range AllContrastChecks() {
		key := [2]Role{chk.FG, chk.BG}
		if min, ok := want[key]; ok {
			if chk.Min < min {
				t.Errorf("%s/%s threshold %.1f, want >= %.1f", chk.FG, chk.BG, chk.Min, min)
			}
			delete(want, key)
		}
	}
	for key := range want {
		t.Errorf("AllContrastChecks lacks a row for %s on %s", key[0], key[1])
	}
}
