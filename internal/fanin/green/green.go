// Package green provides a small, dependency-free helper for counting the
// letter g in a string, ignoring case. It is a leaf package: it imports only
// the standard library.
package green

// Count returns the number of times the byte 'g' appears in s, counting both
// 'g' and 'G'. Every other byte, including the bytes of non-ASCII runes and
// of malformed UTF-8, never matches. Count("") returns 0.
func Count(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == 'g' || s[i] == 'G' {
			n++
		}
	}
	return n
}
