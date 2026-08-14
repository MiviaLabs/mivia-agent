package sliceutil

// Unique returns a new slice holding the values of in with duplicates
// removed, preserving the order of first occurrence. The result is freshly
// allocated, so mutating it never mutates in.
//
// Unique returns nil when in is nil or empty.
func Unique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
