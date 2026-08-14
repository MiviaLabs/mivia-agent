package strops

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestDedup is the table-driven behavior suite for Dedup. It covers the
// success path, nil and empty inputs, single-element input, adjacent and
// non-adjacent duplicates, all-identical input, and empty-string elements.
func TestDedup(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil input", nil, nil},
		{"empty input", []string{}, nil},
		{"single element", []string{"only"}, []string{"only"}},
		{"no duplicates", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"adjacent duplicates", []string{"a", "a", "b", "b", "c"}, []string{"a", "b", "c"}},
		{"non-adjacent duplicates", []string{"a", "b", "a", "c", "b", "d"}, []string{"a", "b", "c", "d"}},
		{"all identical", []string{"x", "x", "x"}, []string{"x"}},
		{"empty string elements", []string{"", "", "a", ""}, []string{"", "a"}},
		{"empties mixed with duplicates", []string{"", "b", "", "b", "c"}, []string{"", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Dedup(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Dedup(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestDedupPreservesFirstOccurrenceOrder pins the order contract: the first
// occurrence of every value keeps its relative position in the result.
func TestDedupPreservesFirstOccurrenceOrder(t *testing.T) {
	in := []string{"z", "y", "x", "z", "y"}
	got := Dedup(in)
	want := []string{"z", "y", "x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Dedup(%q) = %q, want %q", in, got, want)
	}
}

// TestDedupOversizedInput checks that a large input with many duplicates stays
// correct and bounded: the result holds exactly the distinct keys in
// first-occurrence order.
func TestDedupOversizedInput(t *testing.T) {
	const total, distinct = 100_000, 1000
	in := make([]string, total)
	for i := range in {
		in[i] = fmt.Sprintf("key-%d", i%distinct)
	}
	got := Dedup(in)
	if len(got) != distinct {
		t.Fatalf("len(Dedup(...)) = %d, want %d", len(got), distinct)
	}
	for i := 0; i < distinct; i++ {
		if want := fmt.Sprintf("key-%d", i); got[i] != want {
			t.Fatalf("got[%d] = %q, want %q (first-occurrence order broken)", i, got[i], want)
		}
	}
}

// TestDedupDoesNotAliasInput checks that mutating the result never mutates
// the input slice.
func TestDedupDoesNotAliasInput(t *testing.T) {
	in := []string{"a", "b", "a"}
	got := Dedup(in)
	got[0] = "mutated"
	if in[0] != "a" {
		t.Errorf("mutating result changed input: in[0] = %q, want %q", in[0], "a")
	}
}

// FuzzDedup asserts the Dedup contract for arbitrary input: the result is
// duplicate-free, holds exactly the distinct input values, and is a
// subsequence of the input (first-occurrence order). Go fuzzing does not
// support []string arguments, so the corpus is a comma-split string, which
// still exercises empty, single, duplicate, and oversized element lists.
func FuzzDedup(f *testing.F) {
	for _, seed := range []string{
		"",
		",",
		"a",
		"a,a,b,b,c",
		"a,b,a,c,b,d",
		"z,y,x,z,y",
		",,a,",
		"dup,unique,dup",
		strings.Repeat("k,", 4096) + "k",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		in := strings.Split(s, ",")
		got := Dedup(in)
		if len(in) > 0 && len(got) == 0 {
			t.Fatalf("Dedup(%q) = empty for non-empty input", in)
		}
		// The result must be duplicate-free.
		seen := make(map[string]bool, len(got))
		for _, v := range got {
			if seen[v] {
				t.Fatalf("Dedup(%q) contains duplicate %q", in, v)
			}
			seen[v] = true
		}
		// The result must equal the first occurrences in input order.
		var first []string
		seenFirst := make(map[string]bool)
		for _, v := range in {
			if !seenFirst[v] {
				seenFirst[v] = true
				first = append(first, v)
			}
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("Dedup(%q) = %q, want %q", in, got, first)
		}
	})
}
