// Package envutil provides small, dependency-free helpers for parsing
// environment-style string values. It is a leaf package: it imports only the
// standard library.
package envutil

import "strings"

// ParseBool parses s as a boolean, returning def when s does not name a
// recognized value.
//
// The true values are "1", "true", "yes", and "on"; the false values are
// "0", "false", "no", and "off". Matching is case-insensitive, so "YES" and
// "Yes" are accepted. Surrounding whitespace is significant: " true" and
// "true " are unrecognized and return def. ParseBool("", def) returns def.
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
