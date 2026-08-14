package sliceutil

import (
	"slices"
	"testing"
)

// concatChunks flattens chunks back into one slice for equality checks.
func concatChunks[T any](chunks [][]T) []T {
	var out []T
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

// TestChunkSlice covers size 0, size 1, exact multiples, a shorter last
// chunk, and sizes larger than or equal to the input.
func TestChunkSlice(t *testing.T) {
	cases := []struct {
		name  string
		items []int
		size  int
		want  [][]int
	}{
		{"size-zero", []int{1, 2}, 0, nil},
		{"size-negative", []int{1, 2}, -3, nil},
		{"empty", nil, 3, nil},
		{"size-one", []int{1, 2, 3}, 1, [][]int{{1}, {2}, {3}}},
		{"exact-multiple", []int{1, 2, 3, 4}, 2, [][]int{{1, 2}, {3, 4}}},
		{"shorter-last", []int{1, 2, 3, 4, 5}, 2, [][]int{{1, 2}, {3, 4}, {5}}},
		{"size-larger-than-input", []int{1, 2}, 5, [][]int{{1, 2}}},
		{"size-equals-input", []int{1, 2, 3}, 3, [][]int{{1, 2, 3}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ChunkSlice(c.items, c.size)
			if len(got) != len(c.want) {
				t.Fatalf("ChunkSlice(%v, %d) has %d chunks, want %d", c.items, c.size, len(got), len(c.want))
			}
			for i := range got {
				if !slices.Equal(got[i], c.want[i]) {
					t.Errorf("chunk %d = %v, want %v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestChunkSliceOversizedInput checks a large input with a small chunk size.
func TestChunkSliceOversizedInput(t *testing.T) {
	const n = 10000
	const size = 7
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	chunks := ChunkSlice(items, size)
	if len(chunks) != (n+size-1)/size {
		t.Errorf("ChunkSlice(len %d, %d) has %d chunks, want %d", n, size, len(chunks), (n+size-1)/size)
	}
	for i, c := range chunks {
		if len(c) < 1 || len(c) > size {
			t.Errorf("chunk %d has length %d, want 1..%d", i, len(c), size)
		}
	}
	if !slices.Equal(concatChunks(chunks), items) {
		t.Error("concatenated chunks do not equal the input")
	}
}

// TestChunkSliceNoMutation checks that chunking does not mutate the input.
func TestChunkSliceNoMutation(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	orig := slices.Clone(items)
	ChunkSlice(items, 2)
	if !slices.Equal(items, orig) {
		t.Errorf("input mutated: %v, want %v", items, orig)
	}
}

// TestDedupe covers empty, no-duplicate, all-duplicate, and interleaved
// duplicate inputs, preserving first-seen order.
func TestDedupe(t *testing.T) {
	cases := []struct {
		name  string
		items []int
		want  []int
	}{
		{"empty-nil", nil, nil},
		{"empty", []int{}, nil},
		{"single-element", []int{9}, []int{9}},
		{"no-duplicates", []int{1, 2, 3}, []int{1, 2, 3}},
		{"all-duplicates", []int{7, 7, 7}, []int{7}},
		{"interleaved", []int{1, 2, 1, 3, 2, 1}, []int{1, 2, 3}},
		{"first-seen-order", []int{3, 1, 3, 2, 1}, []int{3, 1, 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Dedupe(c.items); !slices.Equal(got, c.want) {
				t.Errorf("Dedupe(%v) = %v, want %v", c.items, got, c.want)
			}
		})
	}
}

// TestDedupeNoMutation checks that the input slice is unchanged.
func TestDedupeNoMutation(t *testing.T) {
	items := []int{1, 2, 1, 3}
	orig := slices.Clone(items)
	Dedupe(items)
	if !slices.Equal(items, orig) {
		t.Errorf("input mutated: %v, want %v", items, orig)
	}
}

// TestDedupeOversizedInput checks large inputs, distinct and identical.
func TestDedupeOversizedInput(t *testing.T) {
	const n = 10000
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	if got := Dedupe(items); len(got) != n {
		t.Errorf("Dedupe of %d distinct elements = %d elements", n, len(got))
	}
	for i := range items {
		items[i] = 1
	}
	if got := Dedupe(items); len(got) != 1 || got[0] != 1 {
		t.Errorf("Dedupe of identical elements = %v, want [1]", got)
	}
}

// hasDuplicates reports whether bs contains a repeated byte.
func hasDuplicates(bs []byte) bool {
	seen := make(map[byte]struct{}, len(bs))
	for _, b := range bs {
		if _, ok := seen[b]; ok {
			return true
		}
		seen[b] = struct{}{}
	}
	return false
}

// isSubsequence reports whether sub appears in items with order preserved.
func isSubsequence(sub, items []byte) bool {
	i := 0
	for _, v := range items {
		if i < len(sub) && sub[i] == v {
			i++
		}
	}
	return i == len(sub)
}

// FuzzChunkSlice checks chunk invariants: non-empty chunks, chunk size
// bounds, chunk count, and lossless concatenation.
func FuzzChunkSlice(f *testing.F) {
	f.Add([]byte("abcdef"), uint32(2))
	f.Add([]byte("x"), uint32(5))
	f.Add([]byte(nil), uint32(0))
	f.Fuzz(func(t *testing.T, items []byte, m uint32) {
		size := int(m % uint32(len(items)+2))
		chunks := ChunkSlice(items, size)
		if size <= 0 || len(items) == 0 {
			if chunks != nil {
				t.Fatalf("ChunkSlice(%q, %d) = %v, want nil", items, size, chunks)
			}
			return
		}
		if len(chunks) != (len(items)-1)/size+1 {
			t.Fatalf("ChunkSlice(%q, %d) has %d chunks, want %d", items, size, len(chunks), (len(items)-1)/size+1)
		}
		for i, c := range chunks {
			if len(c) == 0 {
				t.Fatalf("ChunkSlice(%q, %d) chunk %d is empty", items, size, i)
			}
			if len(c) > size {
				t.Fatalf("ChunkSlice(%q, %d) chunk %d has length %d > %d", items, size, i, len(c), size)
			}
		}
		if !slices.Equal(concatChunks(chunks), items) {
			t.Fatalf("ChunkSlice(%q, %d) does not reassemble the input", items, size)
		}
	})
}

// FuzzDedupe checks the defining properties: no duplicates in the result,
// first-seen order preserved, and every input element present once.
func FuzzDedupe(f *testing.F) {
	f.Add([]byte("a"))
	f.Add([]byte("abac"))
	f.Add([]byte{})
	f.Add([]byte("zzz"))
	f.Fuzz(func(t *testing.T, items []byte) {
		got := Dedupe(items)
		if len(items) == 0 {
			if got != nil {
				t.Fatalf("Dedupe(%q) = %v, want nil", items, got)
			}
			return
		}
		if hasDuplicates(got) {
			t.Fatalf("Dedupe(%q) = %q contains duplicates", items, got)
		}
		if !isSubsequence(got, items) {
			t.Fatalf("Dedupe(%q) = %q does not preserve first-seen order", items, got)
		}
		want := make(map[byte]struct{}, len(items))
		for _, v := range items {
			want[v] = struct{}{}
		}
		gotSet := make(map[byte]struct{}, len(got))
		for _, v := range got {
			gotSet[v] = struct{}{}
		}
		if len(gotSet) != len(want) {
			t.Fatalf("Dedupe(%q) = %q covers %d distinct elements, want %d", items, got, len(gotSet), len(want))
		}
		for v := range want {
			if _, ok := gotSet[v]; !ok {
				t.Fatalf("Dedupe(%q) = %q is missing element %q", items, got, v)
			}
		}
	})
}
