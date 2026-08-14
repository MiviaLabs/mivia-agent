// Package runeutil provides a small, dependency-free helper for counting
// user-perceived characters approximately. It is a leaf package: it imports
// only the standard library.
package runeutil

import "unicode"

// CountGraphemesApprox counts the user-perceived characters in s
// approximately: it counts runes and treats Unicode combining marks
// (nonspacing, spacing, and enclosing marks) as zero-width, so a base letter
// followed by any number of combining marks counts as one character. For
// example, the decomposed form of "café" (the letters c, a, f, e plus the
// U+0301 combining acute accent) counts as 4, not 5.
//
// The count is approximate on purpose: it is not a full Unicode grapheme
// cluster algorithm, so sequences that join with format characters, such as
// emoji ZWJ families or flag pairs, count each rune separately. Control
// characters and every other rune count as one each. CountGraphemesApprox("")
// returns 0.
func CountGraphemesApprox(s string) int {
	n := 0
	for _, r := range s {
		if unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me) {
			continue
		}
		n++
	}
	return n
}
