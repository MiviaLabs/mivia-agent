// Package envutil provides a small, dependency-free helper for parsing
// environment-variable style boolean values. It is a leaf package: it imports
// only the standard library.
package envutil

import "strings"

// ParseBool parses s as a boolean value the way an environment variable would
// be read. The recognized true forms are "1", "true", "yes", and "on"; the
// recognized false forms are "0", "false", "no", and "off". Matching is
// case-insensitive: "TRUE", "Yes", and "On" parse exactly like their
// lowercase forms.
//
// Any other string returns def, so an empty or unrecognized value falls back
// to the caller's default instead of erroring. Matching is exact apart from
// case: leading or trailing whitespace is not stripped, so " true " is
// unrecognized and returns def.
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
