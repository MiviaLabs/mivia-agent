// Package maputil provides small, dependency-free helpers for working with
// maps. It is a leaf package: it imports only the standard library.
package maputil

import "sort"

// SortedKeys returns the keys of m in ascending lexical order. The result is
// freshly allocated, so mutating it never mutates m.
//
// SortedKeys returns nil when m is empty or nil.
func SortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
