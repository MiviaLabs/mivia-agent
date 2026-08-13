// Package textutil provides small, dependency-free string-safety
// primitives shared by packages that must not depend on each other: rune-safe
// byte-cap truncation (jschema, delivery) and control-byte detection
// (workflows/controller, workflows/compiler).
// It also provides ASCII-folded containment (ContainsFold).
// Each importer needs the exact same check; this leaf package holds the one
// implementation.
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

// ellipsisMarker is the ellipsis rune "…" (U+2026) in its UTF-8 form. It is
// exactly one rune and three bytes.
const ellipsisMarker = "\u2026"

// TruncateEllipsis truncates s to a byte budget and appends the ellipsis rune
// "…" (U+2026, 3 bytes) when truncation happens and the budget can fit the
// marker.
//
// When len(s) <= maxBytes, it returns s unchanged with no marker. When
// maxBytes <= 0, it returns "". When maxBytes < 3, the marker cannot fit, so
// it falls back to TruncateRuneSafe(s, maxBytes) with no marker. Otherwise it
// appends "…" to the longest prefix of s that fits maxBytes-3 bytes on a UTF-8
// rune boundary. The result never exceeds maxBytes. For valid UTF-8 input, the
// result is valid UTF-8.
func TruncateEllipsis(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes < len(ellipsisMarker) {
		return TruncateRuneSafe(s, maxBytes)
	}
	return TruncateRuneSafe(s, maxBytes-len(ellipsisMarker)) + ellipsisMarker
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
