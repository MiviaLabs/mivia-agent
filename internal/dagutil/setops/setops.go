// Package setops provides small string-slice set operations for DAG
// construction. It is a leaf package: it imports only the standard library.
package setops

import "sort"

// Union returns the sorted union of a and b: every string present in either
// slice, deduplicated, in ascending lexical order. Neither input is modified
// and the result never aliases either operand.
//
// Union returns nil when the union is empty.
func Union(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, s := range a {
		seen[s] = struct{}{}
	}
	for _, s := range b {
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
