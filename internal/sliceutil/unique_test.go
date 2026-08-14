package sliceutil

import (
	"reflect"
	"strconv"
	"testing"
)

// TestUnique checks the order-preserving duplicate-removal contract.
// It covers nil, empty, and populated inputs, including empty-string
// elements.
func TestUnique(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil input", nil, nil},
		{"empty input", []string{}, nil},
		{"no duplicates", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"adjacent duplicates", []string{"a", "a", "b", "b", "c"}, []string{"a", "b", "c"}},
		{"non-adjacent duplicates", []string{"a", "b", "a", "c", "b", "d"}, []string{"a", "b", "c", "d"}},
		{"all identical", []string{"x", "x", "x", "x"}, []string{"x"}},
		{"empty string preserved", []string{"", "a", "", "b"}, []string{"", "a", "b"}},
		{"leading and trailing duplicates", []string{"a", "a", "b", "c", "a"}, []string{"a", "b", "c"}},
	}
	for _, c := range cases {
		if got := Unique(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Unique(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// TestUniqueOversizedInput checks that a large input with repeats collapses to
// exactly the distinct count. The result must keep first-seen order.
func TestUniqueOversizedInput(t *testing.T) {
	const (
		distinct = 1000
		repeats  = 100
	)
	in := make([]string, 0, distinct*repeats)
	for r := 0; r < repeats; r++ {
		for d := 0; d < distinct; d++ {
			in = append(in, strconv.Itoa(d))
		}
	}
	got := Unique(in)
	if len(got) != distinct {
		t.Fatalf("Unique(%d entries) returned %d entries, want %d distinct", len(in), len(got), distinct)
	}
	for i, v := range got {
		if want := strconv.Itoa(i); v != want {
			t.Fatalf("Unique[%d] = %q, want %q (first-seen order)", i, v, want)
		}
	}
}

// TestUniqueDoesNotAliasInput checks that mutating the returned slice never
// mutates the input. It covers inputs with and without duplicates.
func TestUniqueDoesNotAliasInput(t *testing.T) {
	inputs := [][]string{
		{"a", "b", "c"},
		{"a", "b", "a", "c"},
	}
	for _, in := range inputs {
		original := append([]string(nil), in...)
		got := Unique(in)
		got[0] = "mutated"
		if !reflect.DeepEqual(in, original) {
			t.Errorf("mutating result changed input: in = %v, want %v", in, original)
		}
	}
}

// FuzzUnique checks the deduplication properties on arbitrary input.
// The result must have no duplicates, keep first-seen order, and have a
// length equal to the distinct count. Unique must be idempotent and must
// never alias the input.
func FuzzUnique(f *testing.F) {
	// Seeds cover adjacent and non-adjacent duplicates, all-empty input,
	// empty-string elements, unicode, and a duplicate-free input.
	f.Add("a", "a", "b", "c", "d")
	f.Add("a", "b", "a", "c", "b")
	f.Add("", "", "", "", "")
	f.Add("x", "", "y", "", "z")
	f.Add("日本語", "日本語", "a", "a", "b")
	f.Add("a", "b", "c", "d", "e")
	f.Fuzz(func(t *testing.T, a, b, c, d, e string) {
		in := []string{a, b, c, d, e}
		original := append([]string(nil), in...)

		firstSeen := make(map[string]int, len(in))
		for i, v := range in {
			if _, ok := firstSeen[v]; !ok {
				firstSeen[v] = i
			}
		}
		got := Unique(in)
		if len(got) != len(firstSeen) {
			t.Fatalf("Unique(%v) has %d entries, want %d distinct", in, len(got), len(firstSeen))
		}
		prev := -1
		for _, v := range got {
			pos := firstSeen[v]
			if pos <= prev {
				t.Fatalf("Unique(%v) = %v: %q at first-seen %d breaks first-seen order", in, got, v, pos)
			}
			prev = pos
		}
		if twice := Unique(got); !reflect.DeepEqual(twice, got) {
			t.Fatalf("Unique(Unique(%v)) = %v, want %v", in, twice, got)
		}
		if len(got) > 0 {
			got[0] = "mutated"
			if !reflect.DeepEqual(in, original) {
				t.Fatalf("mutating result changed input: in = %v, want %v", in, original)
			}
		}
	})
}
