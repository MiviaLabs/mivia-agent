package setops

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// TestUnionTable drives Union over the documented input classes: nil and
// empty operands, disjoint and overlapping sets, duplicates inside one
// operand, duplicates across operands, unsorted inputs, already-sorted
// inputs, single-element operands, empty-string elements, and identical
// operands. The nil/empty and all-duplicate rows are the negative paths: no
// input may panic and an empty union must be nil, not a populated slice.
func TestUnionTable(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want []string
	}{
		{"both nil", nil, nil, nil},
		{"both empty", []string{}, []string{}, nil},
		{"nil and empty", nil, []string{}, nil},
		{"a only", []string{"b", "a"}, nil, []string{"a", "b"}},
		{"b only", nil, []string{"d", "c"}, []string{"c", "d"}},
		{"disjoint", []string{"a", "b"}, []string{"c", "d"}, []string{"a", "b", "c", "d"}},
		{"overlapping", []string{"a", "b", "c"}, []string{"c", "d"}, []string{"a", "b", "c", "d"}},
		{"duplicates within a", []string{"b", "a", "b"}, []string{"c"}, []string{"a", "b", "c"}},
		{"duplicates within b", []string{"a"}, []string{"c", "d", "c"}, []string{"a", "c", "d"}},
		{"duplicates across operands", []string{"a", "a"}, []string{"a", "a"}, []string{"a"}},
		{"unsorted inputs", []string{"z", "a", "m"}, []string{"y", "b"}, []string{"a", "b", "m", "y", "z"}},
		{"already sorted", []string{"a", "b"}, []string{"c"}, []string{"a", "b", "c"}},
		{"single elements", []string{"x"}, []string{"y"}, []string{"x", "y"}},
		{"empty strings", []string{"", "a"}, []string{"", "b"}, []string{"", "a", "b"}},
		{"identical operands", []string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Union(c.a, c.b)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Union(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestUnionDoesNotMutateInputs checks that Union leaves both operands intact
// and returns a freshly allocated result that does not alias either input.
func TestUnionDoesNotMutateInputs(t *testing.T) {
	a := []string{"b", "a"}
	b := []string{"c", "b"}
	wantA := []string{"b", "a"}
	wantB := []string{"c", "b"}

	got := Union(a, b)
	got[0] = "zzz" // mutate the result; must not leak into a or b
	if !reflect.DeepEqual(a, wantA) || !reflect.DeepEqual(b, wantB) {
		t.Fatalf("Union mutated an operand: a=%v b=%v", a, b)
	}
	if &got[0] == &a[0] || &got[0] == &b[0] {
		t.Error("Union result aliases an input element")
	}
}

// TestUnionLarge checks an oversized, duplicate-heavy input: 4096 elements
// per operand drawn from a 16-value alphabet, so the union is small and the
// deduplication and sorting paths are exercised at scale.
func TestUnionLarge(t *testing.T) {
	var a, b []string
	for i := 0; i < 4096; i++ {
		a = append(a, fmt.Sprintf("key-%d", i%16))
		b = append(b, fmt.Sprintf("key-%d", (i+8)%16))
	}
	want := make([]string, 0, 16)
	for i := 0; i < 16; i++ {
		want = append(want, fmt.Sprintf("key-%d", i))
	}
	// Union returns ascending lexical order: "key-10" sorts before "key-2",
	// so the expected slice must be sorted lexicographically, not numerically.
	sort.Strings(want)
	got := Union(a, b)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Union(large) = %v, want %v", got, want)
	}
}

// FuzzUnion cross-checks Union against an independent reference
// implementation over arbitrary two-slice inputs derived from a raw seed.
// Strings cross the host boundary (CLI args, config values), so a panic or a
// contract violation on any input would be a reachable defect.
func FuzzUnion(f *testing.F) {
	f.Add("")
	f.Add("a,b,c")
	f.Add("a,b,a;c,d,c")
	f.Add("z,a,m;y,b")
	f.Add("a;a")
	f.Add(";a,b")
	f.Add("a,b;")
	f.Add("x,y;z")
	f.Add(strings.Repeat("a,", 256))
	f.Add(";;")
	f.Fuzz(func(t *testing.T, raw string) {
		a, b := splitUnionSeed(raw)
		got := Union(a, b)
		if want := referenceUnion(a, b); !slices.Equal(got, want) {
			t.Fatalf("Union(%q, %q) = %v, want %v", a, b, got, want)
		}
		checkUnionInvariants(t, a, b, got)
	})
}

// splitUnionSeed turns a raw fuzz seed into the two operand slices: the part
// before the first ';' becomes a and the part after becomes b; each part is
// split on ','. A missing side produces a nil operand.
func splitUnionSeed(raw string) (a, b []string) {
	left, right, _ := strings.Cut(raw, ";")
	return splitSide(left), splitSide(right)
}

func splitSide(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// referenceUnion is an independent, obviously-correct implementation used to
// cross-check Union: it collects both slices into a map and sorts the keys.
func referenceUnion(a, b []string) []string {
	seen := make(map[string]struct{})
	for _, s := range a {
		seen[s] = struct{}{}
	}
	for _, s := range b {
		seen[s] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// checkUnionInvariants asserts the contract of Union directly: the result is
// sorted, has no duplicates, contains every element of both operands, and
// contains nothing else.
func checkUnionInvariants(t *testing.T, a, b, got []string) {
	t.Helper()
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("Union(%q, %q) not sorted: %v", a, b, got)
		}
		if got[i-1] == got[i] {
			t.Fatalf("Union(%q, %q) has duplicates: %v", a, b, got)
		}
	}
	for _, s := range append(append([]string(nil), a...), b...) {
		if !slices.Contains(got, s) {
			t.Fatalf("Union(%q, %q) = %v missing operand element %q", a, b, got, s)
		}
	}
	for _, s := range got {
		if !slices.Contains(a, s) && !slices.Contains(b, s) {
			t.Fatalf("Union(%q, %q) = %v has extra element %q", a, b, got, s)
		}
	}
}
