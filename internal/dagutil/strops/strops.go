// Package strops provides small, dependency-free string helpers used by the
// dagutil package chain. It is a leaf package: it imports only the standard
// library.
package strops

// Dedup returns a new slice holding the values of a with duplicates removed,
// preserving the order of first occurrence. The result is freshly allocated,
// so mutating it never mutates a.
//
// Dedup returns nil when a is nil or empty.
func Dedup(a []string) []string {
	if len(a) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(a))
	out := make([]string, 0, len(a))
	for _, v := range a {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
