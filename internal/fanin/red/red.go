// Package red provides a small, dependency-free counter for the byte 'r'.
// It is a leaf package: it imports only the standard library.
package red

import "strings"

// Count returns the number of times the byte 'r' appears in s, ignoring ASCII
// case: both 'r' and 'R' are counted. The scan is byte-based, so non-ASCII
// runes such as 'é' or 'ŕ' never match.
//
// Count("") returns 0.
func Count(s string) int {
	return strings.Count(s, "r") + strings.Count(s, "R")
}
