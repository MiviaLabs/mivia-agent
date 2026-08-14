// Package runeutil provides small, dependency-free helpers for working with
// Unicode text at the rune level. It is a leaf package: it imports only the
// standard library.
package runeutil

import "unicode"

// CountGraphemesApprox returns the number of user-perceived characters in s,
// approximately. The function counts runes and treats zero-width combining
// marks as width zero. A zero-width combining mark is a rune in the Unicode
// category Mn (nonspacing mark) or Me (enclosing mark). Spacing combining
// marks (category Mc) and all other runes count as one character each.
//
// The result is an approximation of grapheme clusters, not a true cluster
// count. The function does not interpret zero-width joiners (U+200D),
// variation selectors beyond their Mn/Me membership, or regional indicator
// pairs, so multi-codepoint emoji and other clusters count as their raw rune
// count minus zero-width combining marks.
//
// The function never returns a negative value. CountGraphemesApprox("")
// returns 0. Invalid UTF-8 bytes each count as one replacement rune.
func CountGraphemesApprox(s string) int {
	n := 0
	for _, r := range s {
		if unicode.In(r, unicode.Mn, unicode.Me) {
			continue
		}
		n++
	}
	return n
}
