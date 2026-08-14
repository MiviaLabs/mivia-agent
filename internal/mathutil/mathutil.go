// Package mathutil provides small, dependency-free numeric helpers:
// clamping a value to a range and linear interpolation. Every helper is a
// pure function with no state and no external dependencies.
package mathutil

import "cmp"

// Clamp returns v constrained to the inclusive range [lo, hi].
//
// When v is below lo, it returns lo. When v is above hi, it returns hi.
// Otherwise it returns v unchanged. The caller must pass lo <= hi. When the
// bounds are equal, Clamp returns that bound.
//
// Clamp works with any ordered type.
func Clamp[T cmp.Ordered](v, lo, hi T) T {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ClampInt returns v constrained to the inclusive range [lo, hi] for int
// values. It is the int convenience wrapper around Clamp.
func ClampInt(v, lo, hi int) int {
	return Clamp(v, lo, hi)
}

// Lerp returns the linear interpolation between a and b at position t.
//
// It is computed as a*(1-t) + b*t, so a t value of 0 returns a exactly and a
// t value of 1 returns b exactly; the a + (b-a)*t form would round the
// endpoints away when the bounds differ in scale. The caller may pass t
// outside the range [0, 1]; Lerp extrapolates beyond the endpoints in that
// case.
func Lerp(a, b, t float64) float64 {
	return a*(1-t) + b*t
}
