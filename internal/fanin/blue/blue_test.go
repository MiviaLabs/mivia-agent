package blue

import "testing"

// TestCount covers empty input, no matches, lowercase, uppercase, mixed
// case, repeated matches, single-byte inputs, and multibyte input.
func TestCount(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"no matches", "red green", 0},
		{"lowercase only", "blueberry", 2},
		{"uppercase only", "BLUEBERRY", 2},
		{"mixed case", "BlueBerry", 2},
		{"all matches", "bbBBbb", 6},
		{"single lowercase", "b", 1},
		{"single uppercase", "B", 1},
		{"multibyte input", "b\u00e9b\u00e9", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Count(c.in); got != c.want {
				t.Errorf("Count(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestCountExhaustiveBytes checks every possible single-byte input so the
// whole byte domain is covered without a fuzz target: exactly the two bytes
// 'b' and 'B' count, and the other 254 bytes contribute nothing.
func TestCountExhaustiveBytes(t *testing.T) {
	for b := 0; b < 256; b++ {
		in := string([]byte{byte(b)})
		want := 0
		if b == int('b') || b == int('B') {
			want = 1
		}
		if got := Count(in); got != want {
			t.Errorf("Count(%q) = %d, want %d", in, got, want)
		}
	}
}
