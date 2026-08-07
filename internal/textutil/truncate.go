// Package textutil provides rune-safe byte-cap truncation for UTF-8 text.
//
// jschema and delivery both bound stored or rendered text by byte count, and
// both must never split a UTF-8 rune. This leaf package holds that shared
// logic so neither importer depends on the other.
package textutil

import "unicode/utf8"

// TruncateRuneSafe returns the longest prefix of s that is at most maxBytes
// bytes and ends on a UTF-8 rune boundary.
//
// When len(s) <= maxBytes, it returns s unchanged. When maxBytes <= 0, it
// returns "". A cut inside a rune backs off to the previous rune start, so
// the result is always valid UTF-8 for valid UTF-8 input.
func TruncateRuneSafe(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// TruncateTail returns the longest suffix of s that is at most maxBytes bytes
// and starts on a UTF-8 rune boundary.
//
// When len(s) <= maxBytes, it returns s unchanged. When maxBytes <= 0, it
// returns "". Error chains read wrapper first and root cause last, so the
// tail preserves the root cause for diagnostics.
func TruncateTail(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
