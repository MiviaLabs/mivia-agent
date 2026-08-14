// Package blue provides a leaf helper that counts how often the byte 'b'
// appears in a string, ignoring ASCII case. It imports only the standard
// library and depends on none of the other internal/fanin packages.
package blue

// Count returns the number of times the byte 'b' appears in s, ignoring
// ASCII case: both 'b' and 'B' count and every other byte contributes
// nothing. The input is treated as an opaque byte sequence, so rune
// boundaries are never inspected. Count("") returns 0.
func Count(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 'b' || c == 'B' {
			n++
		}
	}
	return n
}
