package envutil

import (
	"strings"
	"testing"
)

// TestParseBool covers the four true tokens and four false tokens in mixed
// case, plus the negative path: unrecognized input - empty, whitespace,
// partial, oversized, or unrelated - returns def instead of guessing.
func TestParseBool(t *testing.T) {
	cases := []struct {
		name string
		s    string
		def  bool
		want bool
	}{
		{"true token", "true", false, true},
		{"one token", "1", false, true},
		{"yes token", "yes", false, true},
		{"on token", "on", false, true},
		{"true uppercase", "TRUE", false, true},
		{"yes title case", "Yes", false, true},
		{"on uppercase", "ON", false, true},

		{"false token", "false", true, false},
		{"zero token", "0", true, false},
		{"no token", "no", true, false},
		{"off token", "off", true, false},
		{"false uppercase", "FALSE", true, false},
		{"no title case", "No", true, false},
		{"off uppercase", "OFF", true, false},

		{"empty string", "", true, true},
		{"empty string false default", "", false, false},
		{"leading whitespace", " true", true, true},
		{"trailing whitespace", "true ", false, false},
		{"partial token", "tru", true, true},
		{"token with suffix", "yesplease", false, false},
		{"out of range number", "2", true, true},
		{"signed zero", "-0", false, false},
		{"plus one", "+1", false, false},
		{"null", "null", true, true},
		{"unrelated text", "maybe", false, false},
		{"oversized unrecognized", strings.Repeat("x", 10000), true, true},
	}
	for _, c := range cases {
		if got := ParseBool(c.s, c.def); got != c.want {
			t.Errorf("ParseBool(%q, %v) = %v, want %v", c.s, c.def, got, c.want)
		}
	}
}

// FuzzParseBool checks the parse contract over arbitrary input: a recognized
// token maps to the same value regardless of def, and unrecognized input
// returns def unchanged.
func FuzzParseBool(f *testing.F) {
	for _, seed := range []string{"true", "FALSE", "yes", "off", "1", "0", "maybe", "", " on"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		withTrue := ParseBool(s, true)
		withFalse := ParseBool(s, false)
		if withTrue == withFalse {
			return // recognized token: both returned the parsed value.
		}
		if withTrue != true || withFalse != false {
			t.Fatalf("ParseBool(%q): unrecognized token returned (%v, %v), want (true, false)", s, withTrue, withFalse)
		}
	})
}
