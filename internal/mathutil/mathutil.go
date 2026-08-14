// Package mathutil provides small, dependency-free numeric helpers: clamping
// ordered values to an inclusive range and linear interpolation. It is a leaf
// package: it imports only the standard library.
package mathutil

import "cmp"

// Ordered is satisfied by any type that supports Go's < and > operators: all
// integer types, all float types, and string.
type Ordered = cmp.Ordered

// Clamp returns v constrained to the inclusive range [lo, hi]: hi when v
// exceeds hi, lo when v is below lo, and v unchanged otherwise.
//
// When lo is greater than hi, the bounds are swapped first, so the result is
// always inside the span between lo and hi. Clamp never returns a value
// outside [min(lo, hi), max(lo, hi)].
func Clamp[T Ordered](v, lo, hi T) T {
	if lo > hi {
		lo, hi = hi, lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ClampInt returns v constrained to the inclusive range [lo, hi]. It behaves
// exactly like Clamp[int] and is provided for callers that prefer a
// non-generic convenience form.
func ClampInt(v, lo, hi int) int {
	return Clamp(v, lo, hi)
}

// Lerp returns the linear interpolation of a and b at t: a + (b-a)*t.
//
// t == 0 yields a, t == 1 yields b, and t inside (0, 1) yields a point on the
// segment between them. Values of t outside [0, 1] extrapolate linearly past
// the endpoints.
func Lerp(a, b, t float64) float64 {
	return a + (b-a)*t
}
