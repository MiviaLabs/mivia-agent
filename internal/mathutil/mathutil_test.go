package mathutil

import "testing"

// TestClampInt covers in-range, below-min, above-max, equal-bounds, and
// boundary values for the int convenience wrapper.
func TestClampInt(t *testing.T) {
	cases := []struct {
		v    int
		lo   int
		hi   int
		want int
	}{
		{5, 0, 10, 5},   // in range
		{-3, 0, 10, 0},  // below min
		{42, 0, 10, 10}, // above max
		{7, 7, 7, 7},    // equal bounds
		{0, 0, 10, 0},   // at min
		{10, 0, 10, 10}, // at max
	}
	for _, c := range cases {
		if got := ClampInt(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("ClampInt(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// TestClampGenericInt covers the same order relations through the generic
// Clamp with an int type argument.
func TestClampGenericInt(t *testing.T) {
	cases := []struct {
		v    int
		lo   int
		hi   int
		want int
	}{
		{5, 0, 10, 5},
		{-3, 0, 10, 0},
		{42, 0, 10, 10},
		{7, 7, 7, 7},
	}
	for _, c := range cases {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// TestClampGenericFloat exercises Clamp with a float64 type argument.
func TestClampGenericFloat(t *testing.T) {
	cases := []struct {
		v    float64
		lo   float64
		hi   float64
		want float64
	}{
		{0.5, 0, 1, 0.5},
		{-0.5, 0, 1, 0},
		{1.5, 0, 1, 1},
		{0.25, 0.25, 0.25, 0.25},
	}
	for _, c := range cases {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%v, %v, %v) = %v, want %v", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// TestClampGenericString exercises Clamp with a string type argument, which
// shows that Clamp accepts any ordered type, not only numbers.
func TestClampGenericString(t *testing.T) {
	cases := []struct {
		v    string
		lo   string
		hi   string
		want string
	}{
		{"b", "a", "c", "b"},
		{"a", "b", "c", "b"},
		{"z", "a", "c", "c"},
	}
	for _, c := range cases {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%q, %q, %q) = %q, want %q", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// TestLerp covers the endpoints, the midpoint, a descending range, and
// extrapolation outside the range [0, 1].
func TestLerp(t *testing.T) {
	cases := []struct {
		a    float64
		b    float64
		t    float64
		want float64
	}{
		{0, 10, 0, 0},        // t=0 returns a
		{0, 10, 1, 10},       // t=1 returns b
		{0, 10, 0.5, 5},      // midpoint
		{10, 0, 0.5, 5},      // descending range
		{2, 4, 2, 6},         // extrapolation above 1
		{2, 4, -1, 0},        // extrapolation below 0
		{1e16, 1.0, 0, 1e16}, // t=0 returns a exactly at the ULP edge
		{1e16, 1.0, 1, 1.0},  // t=1 returns b exactly; a+(b-a) rounds to 0
		{1.0, 1e16, 0, 1.0},  // t=0 returns a exactly when a is small
		{1.0, 1e16, 1, 1e16}, // t=1 returns b exactly when b dominates
	}
	for _, c := range cases {
		if got := Lerp(c.a, c.b, c.t); got != c.want {
			t.Errorf("Lerp(%v, %v, %v) = %v, want %v", c.a, c.b, c.t, got, c.want)
		}
	}
}
