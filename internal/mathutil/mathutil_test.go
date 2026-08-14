package mathutil

import (
	"math"
	"testing"
)

// TestClamp covers in-range, below-min, above-max, and equal-bounds inputs.
func TestClamp(t *testing.T) {
	cases := []struct {
		name string
		v    int
		lo   int
		hi   int
		want int
	}{
		{"in-range", 5, 0, 10, 5},
		{"below-min", -3, 0, 10, 0},
		{"above-max", 12, 0, 10, 10},
		{"equal-low-bound", 0, 0, 10, 0},
		{"equal-high-bound", 10, 0, 10, 10},
		{"equal-bounds", 7, 7, 7, 7},
		{"negative-range", -1, -10, -5, -5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Clamp(c.v, c.lo, c.hi); got != c.want {
				t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
			}
		})
	}
}

// TestClampFloat checks Clamp on float64, including fractional values.
func TestClampFloat(t *testing.T) {
	cases := []struct {
		name      string
		v, lo, hi float64
		want      float64
	}{
		{"in-range", 2.5, 0, 10, 2.5},
		{"below-min", -1.5, 0, 10, 0},
		{"above-max", 11.5, 0, 10, 10},
		{"equal-bounds", 3.25, 3.25, 3.25, 3.25},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Clamp(c.v, c.lo, c.hi); got != c.want {
				t.Errorf("Clamp(%g, %g, %g) = %g, want %g", c.v, c.lo, c.hi, got, c.want)
			}
		})
	}
}

// TestClampString checks Clamp on string values, which are ordered.
func TestClampString(t *testing.T) {
	cases := []struct {
		name      string
		v, lo, hi string
		want      string
	}{
		{"in-range", "b", "a", "z", "b"},
		{"below-min", "0", "a", "z", "a"},
		{"above-max", "{", "a", "z", "z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Clamp(c.v, c.lo, c.hi); got != c.want {
				t.Errorf("Clamp(%q, %q, %q) = %q, want %q", c.v, c.lo, c.hi, got, c.want)
			}
		})
	}
}

// TestClampInt covers the int convenience wrapper.
func TestClampInt(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{5, 0, 10, 5},
		{-3, 0, 10, 0},
		{12, 0, 10, 10},
		{7, 7, 7, 7},
	}
	for _, c := range cases {
		if got := ClampInt(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("ClampInt(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// TestClampLargeValues checks values near the int extremes.
func TestClampLargeValues(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	cases := []struct {
		v, lo, hi, want int
	}{
		{minInt, minInt, maxInt, minInt},
		{maxInt, minInt, maxInt, maxInt},
		{minInt, -5, 5, -5},
		{maxInt, -5, 5, 5},
	}
	for _, c := range cases {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// TestLerp covers t=0, t=1, midpoints, extrapolation, and descending ranges.
func TestLerp(t *testing.T) {
	cases := []struct {
		name    string
		a, b, t float64
		want    float64
	}{
		{"t-zero", 2, 6, 0, 2},
		{"t-one", 2, 6, 1, 6},
		{"midpoint", 2, 6, 0.5, 4},
		{"quarter", 0, 10, 0.25, 2.5},
		{"extrapolate-below", 2, 6, -1, -2},
		{"extrapolate-above", 2, 6, 2, 10},
		{"descending-range", 6, 2, 0.5, 4},
		{"equal-endpoints", 5, 5, 0.5, 5},
		{"negative-endpoints", -10, -2, 0.5, -6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Lerp(c.a, c.b, c.t); got != c.want {
				t.Errorf("Lerp(%g, %g, %g) = %g, want %g", c.a, c.b, c.t, got, c.want)
			}
		})
	}
}

// TestLerpEndpointIdentity locks in the exact endpoint identities for inputs
// where a + (b-a)*t would otherwise round away from the endpoint (MU-1).
func TestLerpEndpointIdentity(t *testing.T) {
	cases := []struct {
		name    string
		a, b, t float64
		want    float64
	}{
		{"t-zero-large-gap", 1e16, 1.0, 0, 1e16},
		{"t-one-large-gap", 1e16, 1.0, 1, 1.0},
		{"t-zero-small-gap", 1.0, 1e16, 0, 1.0},
		{"t-one-small-gap", 1.0, 1e16, 1, 1e16},
		{"t-zero-opposite-signs", -1e16, 1e16, 0, -1e16},
		{"t-one-opposite-signs", -1e16, 1e16, 1, 1e16},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Lerp(c.a, c.b, c.t); got != c.want {
				t.Errorf("Lerp(%g, %g, %g) = %g, want %g", c.a, c.b, c.t, got, c.want)
			}
		})
	}
}

// FuzzClampBounds checks that the result stays inside [lo, hi] and follows
// the boundary rule for lo <= hi.
func FuzzClampBounds(f *testing.F) {
	f.Add(int64(-1), int64(1), int64(0))
	f.Add(int64(0), int64(0), int64(0))
	f.Add(int64(10), int64(0), int64(5))
	f.Fuzz(func(t *testing.T, v, lo, hi int64) {
		if lo > hi {
			lo, hi = hi, lo
		}
		got := Clamp(v, lo, hi)
		if got < lo || got > hi {
			t.Fatalf("Clamp(%d, %d, %d) = %d outside [%d, %d]", v, lo, hi, got, lo, hi)
		}
		switch {
		case v < lo:
			if got != lo {
				t.Fatalf("Clamp(%d, %d, %d) = %d, want lo %d", v, lo, hi, got, lo)
			}
		case v > hi:
			if got != hi {
				t.Fatalf("Clamp(%d, %d, %d) = %d, want hi %d", v, lo, hi, got, hi)
			}
		default:
			if got != v {
				t.Fatalf("Clamp(%d, %d, %d) = %d, want v %d", v, lo, hi, got, v)
			}
		}
	})
}

// FuzzLerpEndpoints checks the exact endpoint identities for finite inputs.
func FuzzLerpEndpoints(f *testing.F) {
	f.Add(float64(0), float64(1))
	f.Add(float64(-5), float64(5))
	f.Add(float64(3.25), float64(-7.5))
	f.Fuzz(func(t *testing.T, a, b float64) {
		if math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) {
			t.Skip("non-finite inputs are out of scope")
		}
		if got := Lerp(a, b, 0); got != a {
			t.Fatalf("Lerp(%g, %g, 0) = %g, want %g", a, b, got, a)
		}
		if got := Lerp(a, b, 1); got != b {
			t.Fatalf("Lerp(%g, %g, 1) = %g, want %g", a, b, got, b)
		}
	})
}
