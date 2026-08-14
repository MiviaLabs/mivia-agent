// Package envutil provides a small, dependency-free helper for parsing
// environment-variable style boolean strings. It is a leaf package: it imports
// only the standard library.
package envutil

import "strings"

// ParseBool parses a common boolean string into a bool.
//
// The strings "1", "true", "yes", and "on" (case-insensitive) parse as true;
// "0", "false", "no", and "off" (case-insensitive) parse as false. Surrounding
// whitespace is ignored. Any other string, including the empty string, returns
// def.
func ParseBool(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
