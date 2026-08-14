// Package strops provides small helpers for string slices. It is a leaf
// package: it imports only the standard library.
package strops

// Dedup returns a new slice holding the elements of a in their original
// order, with duplicates removed: only the first occurrence of each element
// is kept. The input slice a is never modified.
//
// Dedup(nil) returns nil, and Dedup([]string{}) returns a new empty slice.
func Dedup(a []string) []string {
	if len(a) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a))
	out := make([]string, 0, len(a))
	for _, s := range a {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
