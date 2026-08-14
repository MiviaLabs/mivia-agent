package sliceutil

import (
	"reflect"
	"testing"
)

// TestChunkSliceEmpty checks nil and empty inputs produce no chunks.
func TestChunkSliceEmpty(t *testing.T) {
	if got := ChunkSlice[int](nil, 3); got != nil {
		t.Errorf("ChunkSlice(nil, 3) = %v, want nil", got)
	}
	if got := ChunkSlice([]int{}, 3); got != nil {
		t.Errorf("ChunkSlice([], 3) = %v, want nil", got)
	}
}

// TestChunkSliceSizeZero checks a non-positive size produces no chunks, even
// for a non-empty input.
func TestChunkSliceSizeZero(t *testing.T) {
	items := []int{1, 2, 3}
	for _, size := range []int{0, -1, -5} {
		if got := ChunkSlice(items, size); got != nil {
			t.Errorf("ChunkSlice(%v, %d) = %v, want nil", items, size, got)
		}
	}
}

// TestChunkSliceSizeOne checks every element becomes its own chunk.
func TestChunkSliceSizeOne(t *testing.T) {
	got := ChunkSlice([]int{1, 2, 3}, 1)
	want := [][]int{{1}, {2}, {3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChunkSlice([1 2 3], 1) = %v, want %v", got, want)
	}
}

// TestChunkSliceExactMultiple checks that when the length is an exact multiple
// of size every chunk is full and there is no trailing short chunk.
func TestChunkSliceExactMultiple(t *testing.T) {
	got := ChunkSlice([]int{1, 2, 3, 4}, 2)
	want := [][]int{{1, 2}, {3, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChunkSlice([1 2 3 4], 2) = %v, want %v", got, want)
	}
}

// TestChunkSliceLastChunkShorter checks that a remainder becomes a shorter
// final chunk.
func TestChunkSliceLastChunkShorter(t *testing.T) {
	got := ChunkSlice([]int{1, 2, 3, 4, 5}, 2)
	want := [][]int{{1, 2}, {3, 4}, {5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChunkSlice([1 2 3 4 5], 2) = %v, want %v", got, want)
	}
}

// TestChunkSliceSizeLargerThanInput checks that a size larger than the input
// yields a single chunk holding all elements.
func TestChunkSliceSizeLargerThanInput(t *testing.T) {
	got := ChunkSlice([]string{"a", "b"}, 10)
	want := [][]string{{"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChunkSlice([a b], 10) = %v, want %v", got, want)
	}
}

// TestChunkSliceDoesNotAliasInput checks that mutating a returned chunk never
// mutates the input slice.
func TestChunkSliceDoesNotAliasInput(t *testing.T) {
	items := []int{1, 2, 3, 4}
	chunks := ChunkSlice(items, 2)
	chunks[0][0] = 99
	if items[0] != 1 {
		t.Errorf("mutating chunk changed input: items[0] = %d, want 1", items[0])
	}
}

// TestDedupeEmpty checks nil and empty inputs produce nil.
func TestDedupeEmpty(t *testing.T) {
	if got := Dedupe[int](nil); got != nil {
		t.Errorf("Dedupe(nil) = %v, want nil", got)
	}
	if got := Dedupe([]int{}); got != nil {
		t.Errorf("Dedupe([]) = %v, want nil", got)
	}
}

// TestDedupeNoDuplicates checks an input without duplicates is returned in the
// same order.
func TestDedupeNoDuplicates(t *testing.T) {
	items := []int{3, 1, 2}
	got := Dedupe(items)
	want := []int{3, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Dedupe(%v) = %v, want %v", items, got, want)
	}
}

// TestDedupeDuplicates checks adjacent and non-adjacent duplicates collapse to
// the first occurrence, preserving first-seen order.
func TestDedupeDuplicates(t *testing.T) {
	cases := []struct {
		name  string
		items []int
		want  []int
	}{
		{"adjacent duplicates", []int{1, 1, 2, 2, 3}, []int{1, 2, 3}},
		{"non-adjacent duplicates", []int{1, 2, 1, 3, 2, 4}, []int{1, 2, 3, 4}},
		{"all identical", []int{7, 7, 7}, []int{7}},
		{"leading duplicate", []int{1, 1, 1, 2}, []int{1, 2}},
	}
	for _, c := range cases {
		if got := Dedupe(c.items); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Dedupe(%v) = %v, want %v", c.name, c.items, got, c.want)
		}
	}
}

// TestDedupeStrings checks the generic behavior on a string element type.
func TestDedupeStrings(t *testing.T) {
	got := Dedupe([]string{"a", "b", "a", "c", "b"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Dedupe([a b a c b]) = %v, want %v", got, want)
	}
}

// TestDedupeDoesNotAliasInput checks that mutating the result never mutates
// the input slice.
func TestDedupeDoesNotAliasInput(t *testing.T) {
	items := []int{1, 2, 3}
	got := Dedupe(items)
	got[0] = 99
	if items[0] != 1 {
		t.Errorf("mutating result changed input: items[0] = %d, want 1", items[0])
	}
}
