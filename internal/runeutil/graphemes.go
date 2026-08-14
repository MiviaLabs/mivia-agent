// Package runeutil provides a small, dependency-free helper for counting
// user-perceived characters approximately. It is a leaf package: it imports
// only the standard library.
package runeutil

import "unicode"

// CountGraphemesApprox returns the approximate number of user-perceived
// characters in s: the number of runes, with combining marks counted as
// zero-width.
//
// A combining mark is a rune in Unicode general category M (a nonspacing,
// spacing combining, or enclosing mark). CountGraphemesApprox does not
// implement full UAX #29 grapheme segmentation: zero-width joiners, emoji
// modifiers, and other format characters each count as one rune, so a
// sequence such as a woman, a zero-width joiner, and a laptop counts as 3
// (approximate).
//
// CountGraphemesApprox("") returns 0.
func CountGraphemesApprox(s string) int {
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.M, r) {
			continue
		}
		n++
	}
	return n
}
