package textutil

import "unicode/utf8"

// middleMarker is the ASCII ellipsis "..." inserted by TruncateMiddle. It is
// exactly three runes and three bytes, deliberately distinct from the U+2026
// ellipsisMarker used by TruncateEllipsis.
const middleMarker = "..."

// TruncateMiddle shortens s to at most maxLen runes by keeping a rune-safe
// prefix and suffix and inserting one ASCII "..." marker between them.
// maxLen counts runes via utf8.RuneCountInString, not bytes.
//
// When the rune count of s is at most maxLen, it returns s unchanged with no
// marker. When maxLen <= 0, it returns "". When maxLen < 3, the marker cannot
// fit, so it returns the first maxLen runes with no marker. Otherwise it
// splits the remaining maxLen-3 runes evenly: the prefix gets
// ceil((maxLen-3)/2) runes and the suffix gets the rest.
//
// The result never exceeds maxLen runes and never splits a rune. For valid
// UTF-8 input, the result is valid UTF-8.
func TruncateMiddle(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return firstRunes(s, maxLen)
	}
	text := maxLen - 3
	prefixRunes := (text + 1) / 2 // ceil split
	suffixRunes := text / 2       // floor split
	return firstRunes(s, prefixRunes) + middleMarker + lastRunes(s, suffixRunes)
}

// firstRunes returns the first k runes of s as a byte prefix. Invalid UTF-8
// bytes decode as one-byte RuneError runes, so the cut never splits a valid
// rune. When s has fewer than k runes, it returns s.
func firstRunes(s string, k int) string {
	if k <= 0 {
		return ""
	}
	end := 0
	for end < len(s) && k > 0 {
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
		k--
	}
	return s[:end]
}

// lastRunes returns the last k runes of s as a byte suffix. Invalid UTF-8
// bytes decode as one-byte RuneError runes, so the cut never splits a valid
// rune. When s has fewer than k runes, it returns s.
func lastRunes(s string, k int) string {
	if k <= 0 {
		return ""
	}
	start := len(s)
	for i := 0; i < k && start > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(s[:start])
		start -= size
	}
	return s[start:]
}
