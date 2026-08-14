// Package envutil provides a small, dependency-free helper for parsing
// boolean environment-style values. It is a leaf package: it imports only the
// standard library.
package envutil

import "strings"

// ParseBool interprets s as a common boolean string and returns the
// corresponding value. The recognized true tokens are "1", "true", "yes",
// and "on"; the recognized false tokens are "0", "false", "no", and "off".
// Matching is case-insensitive, so "TRUE", "Yes", and "ON" are accepted.
//
// Any other value - the empty string, surrounding whitespace, a partial token
// such as "tru", or unrelated text - is unrecognized and returns def. A
// missing or unknown variable therefore fails toward the caller's default
// instead of guessing. ParseBool never returns anything other than def for
// unrecognized input.
func ParseBool(s string, def bool) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
