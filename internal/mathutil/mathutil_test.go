package mathutil

import (
	"math"
	"testing"
)

// TestClampInRange checks that a value at or between the bounds is returned
// unchanged, for integer, float, and string orderings.
func TestClampInRange(t *testing.T) {
	cases := []struct {
		name string
		v    int
		lo   int
		hi   int
		want int
	}{
		{"value at lo", 5, 5, 10, 5},
		{"value at hi", 10, 5, 10, 10},
		{"value inside", 7, 5, 10, 7},
		{"negative bounds", -3, -10, -1, -3},
	}
	for _, c := range cases {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
	if got := Clamp(2.5, 1.5, 3.5); got != 2.5 {
		t.Errorf("Clamp(2.5, 1.5, 3.5) = %v, want 2.5", got)
	}
	if got := Clamp("mid", "aaa", "zzz"); got != "mid" {
		t.Errorf("Clamp(mid, aaa, zzz) = %q, want mid", got)
	}
}

// TestClampBelowMin checks that a value below lo is clamped up to lo.
func TestClampBelowMin(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{3, 5, 10, 5},
		{-1, 0, 100, 0},
		{-20, -10, -1, -10},
		{math.MinInt, 0, 1, 0},
	}
	for _, c := range cases {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
	if got := Clamp(0.5, 1.0, 2.0); got != 1.0 {
		t.Errorf("Clamp(0.5, 1.0, 2.0) = %v, want 1.0", got)
	}
}

// TestClampAboveMax checks that a value above hi is clamped down to hi.
func TestClampAboveMax(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{12, 5, 10, 10},
		{101, 0, 100, 100},
		{-1, -10, -1, -1},
		{math.MaxInt, 0, 1, 1},
	}
	for _, c := range cases {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
	if got := Clamp(9.5, 1.0, 2.0); got != 2.0 {
		t.Errorf("Clamp(9.5, 1.0, 2.0) = %v, want 2.0", got)
	}
}

// TestClampEqualBounds checks that when lo == hi every value clamps to that
// single bound, from below, at, and above it.
func TestClampEqualBounds(t *testing.T) {
	for _, v := range []int{0, 42, 100} {
		if got := Clamp(v, 42, 42); got != 42 {
			t.Errorf("Clamp(%d, 42, 42) = %d, want 42", v, got)
		}
	}
	if got := Clamp(1.25, 3.5, 3.5); got != 3.5 {
		t.Errorf("Clamp(1.25, 3.5, 3.5) = %v, want 3.5", got)
	}
}

// TestClampInvertedBounds checks that lo > hi swaps the bounds so the result
// stays inside the span between them.
func TestClampInvertedBounds(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{5, 10, 2, 5},   // inside the span [2, 10]
		{1, 10, 2, 2},   // below the span clamps to 2
		{11, 10, 2, 10}, // above the span clamps to 10
	}
	for _, c := range cases {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// TestClampInt checks the non-generic convenience wrapper across all four
// required cases.
func TestClampInt(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{7, 5, 10, 7},   // in range
		{3, 5, 10, 5},   // below min
		{12, 5, 10, 10}, // above max
		{8, 8, 8, 8},    // equal bounds
	}
	for _, c := range cases {
		if got := ClampInt(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("ClampInt(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// approxEqual reports whether a and b agree to within 1e-9, used for Lerp
// results that are not exactly representable in binary floating point.
func approxEqual(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

// TestLerp checks endpoints, midpoint, extrapolation, and descending ranges.
func TestLerp(t *testing.T) {
	cases := []struct {
		name string
		a    float64
		b    float64
		t    float64
		want float64
	}{
		{"t zero is a", 3, 9, 0, 3},
		{"t one is b", 3, 9, 1, 9},
		{"midpoint", 0, 10, 0.5, 5},
		{"quarter", 10, 20, 0.25, 12.5},
		{"extrapolate above", 0, 10, 2, 20},
		{"extrapolate below", 0, 10, -1, -10},
		{"descending range", 10, 0, 0.5, 5},
		{"negative values", -5, 5, 0.5, 0},
		{"fractional endpoints", 0.1, 0.3, 0.5, 0.2},
	}
	for _, c := range cases {
		got := Lerp(c.a, c.b, c.t)
		if !approxEqual(got, c.want) {
			t.Errorf("Lerp(%v, %v, %v) = %v, want %v", c.a, c.b, c.t, got, c.want)
		}
	}
}
