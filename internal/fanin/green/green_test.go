package green

import "testing"

// TestCount covers the success and negative cases: empty input, an absent
// letter, lowercase and uppercase matches, mixed case, repeated letters, a
// letter adjacent to g that is not counted, and words with several g bytes.
func TestCount(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"absent letter", "abcdef", 0},
		{"single lowercase", "g", 1},
		{"single uppercase", "G", 1},
		{"leading", "go", 1},
		{"trailing", "dog", 1},
		{"middle", "again", 1},
		{"mixed case", "GreenGauge", 3},
		{"repeated", "ggg", 3},
		{"adjacent letter not counted", "fgh", 1},
		{"lowercase word", "baggage", 3},
		{"uppercase word", "GARAGE", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Count(c.in); got != c.want {
				t.Errorf("Count(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestCountNonASCII checks that multi-byte runes and malformed UTF-8 never
// match: byte-level scanning must not count any byte of a non-ASCII rune.
func TestCountNonASCII(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"accented word", "café", 0},
		{"cyrillic ge", "\u0433", 0}, // г is a different letter from g
		{"greek gamma", "\u03b3", 0}, // γ is a different letter from g
		{"malformed continuation bytes", string([]byte{0x80, 0x80}), 0},
		{"malformed after a real g", "g\xc0\xaf", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Count(c.in); got != c.want {
				t.Errorf("Count(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}
