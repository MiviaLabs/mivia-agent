package envutil

import "testing"

// TestParseBool covers every accepted positive and negative form in mixed and
// upper case, surrounding whitespace, and the unrecognized fallback to def.
func TestParseBool(t *testing.T) {
	cases := []struct {
		name string
		in   string
		def  bool
		want bool
	}{
		{"one true", "1", false, true},
		{"zero false", "0", true, false},
		{"true lower", "true", false, true},
		{"false lower", "false", true, false},
		{"yes lower", "yes", false, true},
		{"no lower", "no", true, false},
		{"on lower", "on", false, true},
		{"off lower", "off", true, false},
		{"mixed case", "TrUe", false, true},
		{"upper case true", "TRUE", false, true},
		{"upper case yes", "YES", false, true},
		{"upper case off", "OFF", true, false},
		{"mixed case no", "nO", true, false},
		{"surrounding spaces", "  true  ", false, true},
		{"tab and newline", "\t1\n", false, true},
		{"empty string def true", "", true, true},
		{"empty string def false", "", false, false},
		{"unrecognized def true", "garbage", true, true},
		{"unrecognized def false", "maybe", false, false},
		{"partial word", "tru", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseBool(c.in, c.def); got != c.want {
				t.Errorf("ParseBool(%q, %t) = %t, want %t", c.in, c.def, got, c.want)
			}
		})
	}
}
