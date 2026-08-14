// Package sliceutil provides small, dependency-free slice helpers:
// fixed-size chunking and first-seen-order deduplication. Every helper is a
// pure function that does not mutate its input.
package sliceutil

// ChunkSlice splits items into consecutive chunks of at most size elements.
// The last chunk may be shorter.
//
// When size <= 0, it returns nil. When items is empty, it returns nil.
// ChunkSlice does not copy the elements; each chunk shares its backing array
// with items.
func ChunkSlice[T any](items []T, size int) [][]T {
	if size <= 0 || len(items) == 0 {
		return nil
	}
	capacity := len(items) / size
	if len(items)%size != 0 {
		capacity++
	}
	chunks := make([][]T, 0, capacity)
	for start := 0; start < len(items); {
		end := start + size
		if size > len(items)-start {
			end = len(items)
		}
		chunks = append(chunks, items[start:end])
		start = end
	}
	return chunks
}

// Dedupe returns a new slice with the duplicates of items removed. The
// result keeps the first-seen order of items. When items is empty, it
// returns nil. Dedupe does not mutate items.
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
