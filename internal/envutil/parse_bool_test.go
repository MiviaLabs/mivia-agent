package envutil

import "testing"

// TestParseBool covers every recognized true and false form, case variants,
// and fallback to the default for unrecognized input in both directions.
func TestParseBool(t *testing.T) {
	cases := []struct {
		name string
		in   string
		def  bool
		want bool
	}{
		// Recognized true forms.
		{"true", "true", false, true},
		{"TRUE", "TRUE", false, true},
		{"True mixed case", "True", false, true},
		{"one", "1", false, true},
		{"yes", "yes", false, true},
		{"YES", "YES", false, true},
		{"Yes mixed case", "Yes", false, true},
		{"on", "on", false, true},
		{"ON", "ON", false, true},
		{"On mixed case", "On", false, true},
		// Recognized false forms.
		{"false", "false", true, false},
		{"FALSE", "FALSE", true, false},
		{"False mixed case", "False", true, false},
		{"zero", "0", true, false},
		{"no", "no", true, false},
		{"NO", "NO", true, false},
		{"No mixed case", "No", true, false},
		{"off", "off", true, false},
		{"OFF", "OFF", true, false},
		{"Off mixed case", "Off", true, false},
		// Unrecognized input falls back to the default, both directions.
		{"empty string default true", "", true, true},
		{"empty string default false", "", false, false},
		{"whitespace only", "  ", true, true},
		{"leading whitespace not trimmed", " true", false, false},
		{"trailing whitespace not trimmed", "true ", true, true},
		{"garbage word", "maybe", true, true},
		{"garbage word false default", "maybe", false, false},
		{"partial token", "tru", true, true},
		{"numeric out of range", "2", true, true},
		{"signed number", "-1", false, false},
		{"oversized input", "this is a very long string that no boolean parser should ever recognize as a boolean value at all", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseBool(c.in, c.def); got != c.want {
				t.Errorf("ParseBool(%q, %t) = %t, want %t", c.in, c.def, got, c.want)
			}
		})
	}
}
