// Package stringutil provides small, dependency-free helpers for string case
// conversion. It is a leaf package: it imports only the standard library.
package stringutil

import (
	"strings"
	"unicode"
)

// ToKebabCase converts a camelCase, PascalCase, snake_case, or
// SCREAMING_SNAKE_CASE string to kebab-case: lowercase words joined by single
// hyphens.
//
// Word boundaries are inserted before an uppercase letter that follows a
// lowercase letter or digit (fooBar -> foo-bar) and before an uppercase letter
// that ends a run of capitals followed by a lowercase letter (HTTPServer ->
// http-server). Underscores, hyphens, and ASCII spaces are separators: runs
// collapse to one hyphen, and leading or trailing separators are dropped.
// Non-ASCII letters are lowercased and never split. Unicode titlecase letters
// (for example ǅ) are treated like capitals and lowercased. Digits stay inside
// the word that contains them.
//
// ToKebabCase("") returns "".
func ToKebabCase(s string) string {
	// prevLower, prevUpper, and endedSep track the previous rune: a lowercase
	// letter or digit, an uppercase letter, or a separator.
	var prevLower, prevUpper, endedSep bool
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == ' ':
			if b.Len() > 0 && !endedSep {
				b.WriteByte('-')
			}
			endedSep = true
			prevLower, prevUpper = false, false
		case unicode.IsUpper(r) || unicode.IsTitle(r):
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if !endedSep && (prevLower || (prevUpper && nextLower)) {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
			endedSep = false
			prevLower, prevUpper = false, true
		default:
			b.WriteRune(r)
			endedSep = false
			prevLower, prevUpper = true, false
		}
	}
	return strings.Trim(b.String(), "-")
}
