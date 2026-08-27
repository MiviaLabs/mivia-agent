package theme

import "testing"

// TestHSLToSRGBKnownValues pins hslToSRGB against known HSL->RGB reference
// points. Nothing else in the package checks the hue/saturation/lightness
// mapping directly: SearchStatusPalette's tests only assert downstream
// contrast/separation properties, which a broken hue mapping could still
// pass by coincidence.
func TestHSLToSRGBKnownValues(t *testing.T) {
	cases := []struct {
		name    string
		h, s, l float64
		wantHex string
	}{
		{"red", 0, 1, 0.5, "#ff0000"},
		{"green", 120, 1, 0.5, "#00ff00"},
		{"blue", 240, 1, 0.5, "#0000ff"},
		{"cyan", 180, 1, 0.5, "#00ffff"},
		{"magenta", 300, 1, 0.5, "#ff00ff"},
		{"yellow", 60, 1, 0.5, "#ffff00"},
		{"white (achromatic, s=0)", 0, 0, 1, "#ffffff"},
		{"black (achromatic, s=0)", 0, 0, 0, "#000000"},
		{"mid-grey (achromatic, s=0)", 200, 0, 0.5, "#808080"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hslToSRGB(c.h, c.s, c.l).hex()
			if got != c.wantHex {
				t.Errorf("hslToSRGB(%v,%v,%v) = %s, want %s", c.h, c.s, c.l, got, c.wantHex)
			}
		})
	}
}
