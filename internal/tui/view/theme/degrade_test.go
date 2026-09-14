package theme

import "testing"

func mustDarkTheme(t *testing.T) Theme {
	t.Helper()
	themes, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark not found in embedded themes")
	return Theme{}
}

// TestResolveDegradationLadder is the golden test per tier: research.md
// finding 8 says a generic nearest-RGB downsample turns the achromatic
// accent to silver (#c0c0c0) at 16 colours. This pins the actual shipped
// resolution at every tier so that regression is caught immediately.
func TestResolveDegradationLadder(t *testing.T) {
	th := mustDarkTheme(t)

	cases := []struct {
		tier Tier
		want Style
	}{
		{TierTrueColor, Style{Hex: "#fafafa", ANSI16: -1, Bold: true}},
		{Tier256, Style{Hex: "#fafafa", ANSI16: -1, Bold: true}},
		{Tier16, Style{ANSI16: 15, Bold: true}}, // NOT silver/8 - the explicit map fix
		{TierASCII, Style{ANSI16: -1, NoColor: true, Bold: true}},
		{TierNoTTY, Style{ANSI16: -1, NoColor: true, Bold: true}},
	}
	for _, c := range cases {
		got := th.Resolve(RoleAccent, c.tier)
		if got != c.want {
			t.Errorf("Resolve(accent, %v) = %+v, want %+v", c.tier, got, c.want)
		}
	}
}

func TestResolveEveryRoleEveryTier(t *testing.T) {
	themes, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	tiers := []Tier{TierTrueColor, Tier256, Tier16, TierASCII, TierNoTTY}
	for _, th := range themes {
		for _, r := range AllRoles() {
			for _, tier := range tiers {
				style := th.Resolve(r, tier)
				switch tier {
				case TierTrueColor, Tier256:
					if style.Hex == "" {
						t.Errorf("%s/%s at %v: expected hex, got none", th.Name, r, tier)
					}
				case Tier16:
					if style.ANSI16 < 0 || style.ANSI16 > 15 {
						t.Errorf("%s/%s at %v: ANSI16 index out of range: %d", th.Name, r, tier, style.ANSI16)
					}
					if style.Hex != "" {
						t.Errorf("%s/%s at %v: expected no hex at 16-colour tier, got %s", th.Name, r, tier, style.Hex)
					}
				case TierASCII, TierNoTTY:
					if !style.NoColor || style.Hex != "" || style.ANSI16 != -1 {
						t.Errorf("%s/%s at %v: expected no-colour style, got %+v", th.Name, r, tier, style)
					}
				}
			}
		}
	}
}

func TestDetectHonoursNoColor(t *testing.T) {
	tier := Detect(discard{}, []string{"NO_COLOR=1"})
	if tier == TierTrueColor {
		t.Fatalf("NO_COLOR should not resolve to TrueColor, got %v", tier)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
