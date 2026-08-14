// Package sliceutil provides small generic slice helpers: fixed-size
// chunking and first-seen-order deduplication. It has no dependencies
// outside the standard library.
package sliceutil

// ChunkSlice splits items into consecutive chunks of at most size elements.
// The last chunk may be shorter. When size <= 0 or items is empty it returns
// nil. The chunks are sub-slices that share the input backing array.
func ChunkSlice[T any](items []T, size int) [][]T {
	if size <= 0 || len(items) == 0 {
		return nil
	}
	chunks := make([][]T, 0, (len(items)-1)/size+1)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		chunks = append(chunks, items[start:end])
	}
	return chunks
}

// Dedupe removes duplicate elements from items and returns a new slice that
// preserves the order of first appearance. It leaves items unchanged. When
// items is empty it returns nil.
func Dedupe[T comparable](items []T) []T {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[T]struct{}, len(items))
	out := make([]T, 0, len(items))
	for _, v := range items {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
