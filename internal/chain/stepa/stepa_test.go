package stepa

import (
	"math"
	"testing"
)

// TestAIncrements checks the success path: zero, positive, and negative
// inputs all increase by exactly one.
func TestAIncrements(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero", 0, 1},
		{"one", 1, 2},
		{"positive", 41, 42},
		{"negative", -1, 0},
		{"negative large", -100, -99},
	}
	for _, c := range cases {
		if got := A(c.in); got != c.want {
			t.Errorf("A(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestABoundary covers the representable limits of int, the only edge where
// the wrapping behavior of A is observable. The minimum value maps to the
// next integer. The maximum value wraps to the minimum value under Go's
// two's-complement arithmetic instead of panicking.
func TestABoundary(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"min int", math.MinInt, math.MinInt + 1},
		{"max int wraps", math.MaxInt, math.MinInt},
	}
	for _, c := range cases {
		if got := A(c.in); got != c.want {
			t.Errorf("A(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
