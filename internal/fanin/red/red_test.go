package red

import "testing"

// TestCount covers success cases (lowercase, uppercase, mixed, repeated) and
// negative paths (empty input, no 'r' present, non-ASCII runes that contain
// no byte equal to 'r').
func TestCount(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"no r", "egg", 0},
		{"single lowercase", "red", 1},
		{"single uppercase", "Red", 1},
		{"all uppercase", "RRR", 3},
		{"both cases", "rR", 2},
		{"repeated lowercase", "rrr", 3},
		{"embedded uppercase", "gRape", 1},
		{"mixed cases", "gRapeRr", 3},
		{"whitespace and punctuation", "r r,r!", 3},
		{"other letters with one r", "green", 1},
		{"non-ascii runes", "café", 0},
		{"long run", "rrrrrrrrrr", 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Count(c.in); got != c.want {
				t.Errorf("Count(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// FuzzCount checks two properties any correct case-insensitive byte counter
// must satisfy: the count never exceeds the string length, and appending the
// byte 'r' or 'R' raises the count by exactly one. The properties are
// independent of the implementation, so the fuzz target is deterministic.
func FuzzCount(f *testing.F) {
	f.Add("")
	f.Add("red")
	f.Add("RRR")
	f.Add("gRapeRr")
	f.Add("café")
	f.Add("r r,R!")
	f.Fuzz(func(t *testing.T, s string) {
		got := Count(s)
		if got < 0 || got > len(s) {
			t.Fatalf("Count(%q) = %d, want value in [0, %d]", s, got, len(s))
		}
		if Count(s+"r") != got+1 {
			t.Fatalf("Count(%q + 'r') = %d, want %d", s, Count(s+"r"), got+1)
		}
		if Count(s+"R") != got+1 {
			t.Fatalf("Count(%q + 'R') = %d, want %d", s, Count(s+"R"), got+1)
		}
	})
}
