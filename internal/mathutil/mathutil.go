// Package mathutil provides small, dependency-free numeric helpers: bounds
// clamping for ordered types and linear interpolation for float64.
package mathutil

import "cmp"

// Clamp returns v clamped to the inclusive range [lo, hi]: lo when v < lo,
// hi when v > hi, and v otherwise. Callers should pass lo <= hi.
func Clamp[T cmp.Ordered](v, lo, hi T) T {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ClampInt clamps an int to the inclusive range [lo, hi]. It is the int
// convenience wrapper for Clamp.
func ClampInt(v, lo, hi int) int {
	return Clamp(v, lo, hi)
}

// Lerp returns the linear interpolation a + (b-a)*t. t is not clamped:
// t=0 yields a exactly, t=1 yields b exactly, and other values, including
// those outside [0,1], extrapolate.
func Lerp(a, b, t float64) float64 {
	if t == 0 {
		return a
	}
	if t == 1 {
		return b
	}
	return a + (b-a)*t
}
