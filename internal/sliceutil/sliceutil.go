// Package sliceutil provides small, dependency-free helpers for slicing
// slices: fixed-size chunking and order-preserving duplicate removal. It is a
// leaf package: it imports only the standard library.
package sliceutil

// ChunkSlice splits items into consecutive chunks of at most size elements.
// Every chunk except the last has exactly size elements; the last chunk may
// be shorter. The returned chunks are freshly allocated copies, so mutating a
// chunk never mutates items.
//
// ChunkSlice returns nil when items is empty or when size <= 0.
func ChunkSlice[T any](items []T, size int) [][]T {
	if size <= 0 || len(items) == 0 {
		return nil
	}
	chunks := make([][]T, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		chunk := make([]T, end-start)
		copy(chunk, items[start:end])
		chunks = append(chunks, chunk)
	}
	return chunks
}

// Dedupe returns a new slice holding the values of items with duplicates
// removed, preserving the order of first occurrence. The result is freshly
// allocated, so mutating it never mutates items.
//
// Dedupe returns nil when items is nil or empty.
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

// Unique returns a new slice holding the values of input with duplicates
// removed, preserving the order of first occurrence. The result is freshly
// allocated, so mutating it never mutates input.
//
// Unique returns nil when input is nil or empty. It is the string-typed
// form of Dedupe.
func Unique(input []string) []string {
	return Dedupe(input)
}
