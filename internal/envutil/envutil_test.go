package envutil

import (
	"strconv"
	"strings"
	"testing"
)

// TestParseBool covers every recognized true and false form, case
// insensitivity, and the unrecognized fallback with both defaults.
func TestParseBool(t *testing.T) {
	type tc struct {
		name string
		in   string
		def  bool
		want bool
	}
	var cases []tc
	for _, form := range []string{"1", "true", "yes", "on"} {
		cases = append(cases, tc{name: "true form " + form, in: form, def: false, want: true})
	}
	for _, form := range []string{"0", "false", "no", "off"} {
		cases = append(cases, tc{name: "false form " + form, in: form, def: true, want: false})
	}
	cases = append(cases,
		tc{name: "empty falls back true", in: "", def: true, want: true},
		tc{name: "empty falls back false", in: "", def: false, want: false},
		tc{name: "unrecognized falls back true", in: "maybe", def: true, want: true},
		tc{name: "unrecognized falls back false", in: "maybe", def: false, want: false},
		tc{name: "numeric garbage falls back", in: "2", def: false, want: false},
		tc{name: "whitespace is significant", in: " true ", def: false, want: false},
		tc{name: "uppercase true", in: "TRUE", def: false, want: true},
		tc{name: "mixed case yes", in: "Yes", def: false, want: true},
		tc{name: "mixed case on", in: "On", def: false, want: true},
		tc{name: "uppercase false", in: "FALSE", def: true, want: false},
		tc{name: "mixed case no", in: "No", def: true, want: false},
		tc{name: "mixed case off", in: "Off", def: true, want: false},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseBool(c.in, c.def); got != c.want {
				t.Errorf("ParseBool(%q, %t) = %t, want %t", c.in, c.def, got, c.want)
			}
		})
	}
}

// FuzzParseBool checks case invariance and that the parsed value re-parses
// from its canonical "true"/"false" form regardless of the default passed.
func FuzzParseBool(f *testing.F) {
	f.Add("true")
	f.Add("FALSE")
	f.Add("On")
	f.Add("maybe")
	f.Add("")
	f.Add("1")
	f.Add(" 0 ")
	f.Fuzz(func(t *testing.T, s string) {
		for _, def := range []bool{true, false} {
			got := ParseBool(s, def)
			if got != ParseBool(strings.ToLower(s), def) {
				t.Fatalf("ParseBool(%q, %t) = %t, but lowercase parses differently",
					s, def, got)
			}
			canonical := ParseBool(strconv.FormatBool(got), !got)
			if canonical != got {
				t.Fatalf("ParseBool(%q, %t) = %t, reparsing canonical form gives %t",
					s, def, got, canonical)
			}
		}
	})
}
