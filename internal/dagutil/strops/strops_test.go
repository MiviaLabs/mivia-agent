package strops

import (
	"slices"
	"testing"
)

// TestDedup covers nil and empty input, duplicate-free input, adjacent and
// non-adjacent duplicates, all-duplicate input, and empty-string elements,
// and checks that first-occurrence order is preserved.
func TestDedup(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil input", nil, nil},
		{"empty input", []string{}, []string{}},
		{"single element", []string{"a"}, []string{"a"}},
		{"no duplicates", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"adjacent duplicates", []string{"a", "a", "b"}, []string{"a", "b"}},
		{"non-adjacent duplicates", []string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{"all duplicates", []string{"x", "x", "x"}, []string{"x"}},
		{"empty string elements", []string{"", "", "a", ""}, []string{"", "a"}},
		{"order preserved", []string{"b", "a", "b", "c", "a"}, []string{"b", "a", "c"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Dedup(c.in); !slices.Equal(got, c.want) {
				t.Errorf("Dedup(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestDedupDoesNotMutateInput checks that Dedup leaves the input slice and
// its elements untouched, so a caller can safely reuse a after a call.
func TestDedupDoesNotMutateInput(t *testing.T) {
	in := []string{"a", "b", "a", "c"}
	want := []string{"a", "b", "a", "c"}
	Dedup(in)
	if !slices.Equal(in, want) {
		t.Errorf("input mutated by Dedup: got %v, want %v", in, want)
	}
}
