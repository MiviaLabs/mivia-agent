// Package runeutil provides a small, dependency-free helper for
// approximating user-perceived character counts. It is a leaf package:
// it imports only the standard library.
package runeutil

import "unicode"

// CountGraphemesApprox returns an approximate count of user-perceived
// characters in s. It walks the runes of s and counts every rune that is not
// a Unicode mark, so combining characters such as U+0301 (combining acute
// accent) contribute zero width: "e\u0301" counts as 1, and the precomposed
// "é" in "café" counts as a single rune like any other.
//
// The count is approximate because it is not a full grapheme-cluster
// segmentation: clusters whose parts are not marks count each part. A family
// emoji written with zero-width joiners counts each emoji and each joiner,
// and an invalid UTF-8 byte decodes as U+FFFD and counts as one character.
func CountGraphemesApprox(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsMark(r) {
			n++
		}
	}
	return n
}
