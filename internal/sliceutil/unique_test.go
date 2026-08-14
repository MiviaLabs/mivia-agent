package sliceutil

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestUniqueEmpty checks nil and empty inputs produce nil, matching the
// sibling Dedupe contract.
func TestUniqueEmpty(t *testing.T) {
	if got := Unique(nil); got != nil {
		t.Errorf("Unique(nil) = %v, want nil", got)
	}
	if got := Unique([]string{}); got != nil {
		t.Errorf("Unique([]) = %v, want nil", got)
	}
}

// TestUnique checks the duplicate-removal contract across a table of cases:
// order preservation, adjacent and non-adjacent collapse, all-identical
// input, and an empty-string element treated as a distinct first-seen value.
func TestUnique(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"no duplicates", []string{"alpha", "beta", "gamma"}, []string{"alpha", "beta", "gamma"}},
		{"adjacent duplicates", []string{"a", "a", "b", "b", "c"}, []string{"a", "b", "c"}},
		{"non-adjacent duplicates", []string{"a", "b", "a", "c", "b", "d"}, []string{"a", "b", "c", "d"}},
		{"all identical", []string{"x", "x", "x"}, []string{"x"}},
		{"empty-string element", []string{"", "a", "", "b", ""}, []string{"", "a", "b"}},
		{"leading duplicate", []string{"a", "a", "a", "b"}, []string{"a", "b"}},
	}
	for _, c := range cases {
		if got := Unique(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Unique(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// TestUniqueOversized checks uniqueness and first-seen order on a 10,000-entry
// input cycling through 100 distinct values, so the map-based dedupe is
// exercised at O(n) with interleaved duplicates.
func TestUniqueOversized(t *testing.T) {
	const n = 10000
	in := make([]string, 0, n)
	for i := 0; i < n; i++ {
		in = append(in, "v"+strconv.Itoa(i%100))
	}
	want := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		want = append(want, "v"+strconv.Itoa(i))
	}
	if got := Unique(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("Unique(oversized) = %d entries, want %d; first mismatch: %v", len(got), len(want), got)
	}
}

// TestUniqueDoesNotAliasInput checks that mutating a result element never
// mutates the input slice (the result is freshly allocated).
func TestUniqueDoesNotAliasInput(t *testing.T) {
	in := []string{"a", "b", "c"}
	got := Unique(in)
	got[0] = "z"
	if in[0] != "a" {
		t.Errorf("mutating result changed input: in[0] = %q, want %q", in[0], "a")
	}
}

// FuzzUnique asserts the dedupe contract on every fuzzed input, using the
// supported string argument type and deriving the []string input from it:
//   - the result contains no duplicate values;
//   - the result is a subsequence of the input preserving first-occurrence
//     order;
//   - len(result) equals the number of distinct values in the input.
func FuzzUnique(f *testing.F) {
	f.Add("")
	f.Add("single")
	f.Add("dup dup heavy heavy heavy")
	f.Add("alpha beta alpha gamma beta")
	f.Add("日本語 日本語 α β α")
	f.Fuzz(func(t *testing.T, s string) {
		in := strings.Fields(s)
		got := Unique(in)

		seen := make(map[string]bool, len(got))
		for _, v := range got {
			if seen[v] {
				t.Fatalf("Unique(%q) = %v contains duplicate %q", s, got, v)
			}
			seen[v] = true
		}

		distinct := make(map[string]bool, len(in))
		idx := 0
		for _, v := range in {
			if distinct[v] {
				continue
			}
			distinct[v] = true
			if idx >= len(got) || got[idx] != v {
				t.Fatalf("Unique(%q) = %v is not the first-occurrence subsequence of %v", s, got, in)
			}
			idx++
		}
		if len(got) != len(distinct) {
			t.Fatalf("Unique(%q) = %v, want %d distinct values", s, got, len(distinct))
		}
	})
}
