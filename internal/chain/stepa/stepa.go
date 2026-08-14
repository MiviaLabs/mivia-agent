// Package stepa is the first link of the internal/chain dependency chain. It
// provides the pure function A and imports only the standard library, so it
// can be built and tested on its own.
package stepa

// A returns n plus one. It has no side effects and never fails, so any
// package that imports stepa can call it.
//
// Integer addition follows Go's two's-complement wrapping rules: A applied to
// the largest representable int wraps to the smallest representable int
// instead of panicking.
func A(n int) int {
	return n + 1
}
