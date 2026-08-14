package sliceutil

// Unique returns a new slice that holds the values of in with duplicates
// removed. The result keeps the order of first occurrence. The result is
// freshly allocated, so mutating it never mutates in.
//
// Unique returns nil when in is nil or empty.
//
// Unique is the string specialization of Dedupe. Callers with a comparable
// element type can use Dedupe directly.
func Unique(in []string) []string {
	return Dedupe(in)
}
