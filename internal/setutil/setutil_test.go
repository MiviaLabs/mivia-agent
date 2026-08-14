package setutil

import "testing"

// makeSet builds a Set from the given elements using the zero value, which
// exercises lazy map allocation on Add.
func makeSet(items ...int) Set[int] {
	var s Set[int]
	for _, v := range items {
		s.Add(v)
	}
	return s
}

// setMembers returns the elements of s in map iteration order for messages.
func setMembers[T comparable](s Set[T]) []T {
	var out []T
	for v := range s.m {
		out = append(out, v)
	}
	return out
}

// assertSetEqual fails the test when got and want hold different elements.
func assertSetEqual(t *testing.T, got, want Set[int]) {
	t.Helper()
	if got.Len() != want.Len() {
		t.Fatalf("set length = %d, want %d (got %v)", got.Len(), want.Len(), setMembers(got))
	}
	for _, v := range setMembers(want) {
		if !got.Contains(v) {
			t.Fatalf("set %v is missing element %d", setMembers(got), v)
		}
	}
}

// TestZeroValueSet checks that the zero value is usable without a constructor.
func TestZeroValueSet(t *testing.T) {
	var s Set[string]
	if s.Len() != 0 {
		t.Errorf("zero-value Len = %d, want 0", s.Len())
	}
	if s.Contains("x") {
		t.Error("zero-value set contains x")
	}
	s.Add("x")
	s.Add("y")
	if s.Len() != 2 || !s.Contains("x") || !s.Contains("y") {
		t.Errorf("set = %v after Add, want {x y}", setMembers(s))
	}
}

// TestAddDuplicateIsNoOp checks that a duplicate Add keeps one element.
func TestAddDuplicateIsNoOp(t *testing.T) {
	s := makeSet(1, 2, 1, 3, 2)
	assertSetEqual(t, s, makeSet(1, 2, 3))
}

// TestRemove checks removal of present and absent elements.
func TestRemove(t *testing.T) {
	s := makeSet(1, 2, 3)
	s.Remove(2)
	assertSetEqual(t, s, makeSet(1, 3))
	s.Remove(99) // absent element is a no-op
	assertSetEqual(t, s, makeSet(1, 3))
}

// TestRemoveFromEmpty checks that Remove on the zero value is a no-op.
func TestRemoveFromEmpty(t *testing.T) {
	var s Set[int]
	s.Remove(1)
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0", s.Len())
	}
}

// TestContains checks membership and absence.
func TestContains(t *testing.T) {
	s := makeSet(1, 2, 3)
	for _, v := range []int{1, 2, 3} {
		if !s.Contains(v) {
			t.Errorf("set does not contain %d", v)
		}
	}
	for _, v := range []int{0, 4, -1} {
		if s.Contains(v) {
			t.Errorf("set contains %d, want absent", v)
		}
	}
}

// TestUnion covers empty, disjoint, and overlapping inputs.
func TestUnion(t *testing.T) {
	cases := []struct {
		name string
		a, b []int
		want []int
	}{
		{"both-empty", nil, nil, nil},
		{"left-empty", nil, []int{1, 2}, []int{1, 2}},
		{"right-empty", []int{1, 2}, nil, []int{1, 2}},
		{"disjoint", []int{1, 2}, []int{3, 4}, []int{1, 2, 3, 4}},
		{"overlapping", []int{1, 2, 3}, []int{2, 3, 4}, []int{1, 2, 3, 4}},
		{"equal", []int{1, 2}, []int{1, 2}, []int{1, 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Union(makeSet(c.a...), makeSet(c.b...))
			assertSetEqual(t, got, makeSet(c.want...))
		})
	}
}

// TestIntersect covers empty, disjoint, overlapping, and equal inputs.
func TestIntersect(t *testing.T) {
	cases := []struct {
		name string
		a, b []int
		want []int
	}{
		{"both-empty", nil, nil, nil},
		{"left-empty", nil, []int{1, 2}, nil},
		{"right-empty", []int{1, 2}, nil, nil},
		{"disjoint", []int{1, 2}, []int{3, 4}, nil},
		{"overlapping", []int{1, 2, 3}, []int{2, 3, 4}, []int{2, 3}},
		{"equal", []int{1, 2}, []int{1, 2}, []int{1, 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Intersect(makeSet(c.a...), makeSet(c.b...))
			assertSetEqual(t, got, makeSet(c.want...))
		})
	}
}

// TestDiff covers empty, disjoint, subset, and overlapping inputs.
func TestDiff(t *testing.T) {
	cases := []struct {
		name string
		a, b []int
		want []int
	}{
		{"both-empty", nil, nil, nil},
		{"left-empty", nil, []int{1, 2}, nil},
		{"right-empty", []int{1, 2}, nil, []int{1, 2}},
		{"disjoint", []int{1, 2}, []int{3, 4}, []int{1, 2}},
		{"b-superset", []int{1, 2}, []int{1, 2, 3}, nil},
		{"overlapping", []int{1, 2, 3}, []int{2, 3, 4}, []int{1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Diff(makeSet(c.a...), makeSet(c.b...))
			assertSetEqual(t, got, makeSet(c.want...))
		})
	}
}

// TestOperationsReturnNewSet checks that the operations do not mutate either
// input and that a caller can Add to each result.
func TestOperationsReturnNewSet(t *testing.T) {
	a := makeSet(1, 2)
	b := makeSet(2, 3)
	u := Union(a, b)
	i := Intersect(a, b)
	d := Diff(a, b)
	u.Add(9)
	i.Add(8)
	d.Add(7)
	assertSetEqual(t, a, makeSet(1, 2))
	assertSetEqual(t, b, makeSet(2, 3))
	assertSetEqual(t, u, makeSet(1, 2, 3, 9))
	assertSetEqual(t, i, makeSet(2, 8))
	assertSetEqual(t, d, makeSet(1, 7))
}

// setFromBytes builds a Set[byte] from raw bytes, allowing duplicates.
func setFromBytes(bs []byte) Set[byte] {
	var s Set[byte]
	for _, b := range bs {
		s.Add(b)
	}
	return s
}

// FuzzSetOps checks the defining properties of Union, Intersect, and Diff
// over arbitrary byte sets, including duplicate bytes in the input.
func FuzzSetOps(f *testing.F) {
	f.Add([]byte("abc"), []byte("bcd"))
	f.Add([]byte(nil), []byte(nil))
	f.Add([]byte("aaa"), []byte("a"))
	f.Add([]byte("x"), []byte(nil))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		sa := setFromBytes(a)
		sb := setFromBytes(b)
		u := Union(sa, sb)
		i := Intersect(sa, sb)
		d := Diff(sa, sb)

		for _, v := range a {
			if !u.Contains(v) {
				t.Fatalf("Union(%q, %q) is missing %q from a", a, b, v)
			}
			both := sb.Contains(v)
			if both != i.Contains(v) {
				t.Fatalf("Intersect(%q, %q): membership of %q = %v, want %v", a, b, v, i.Contains(v), both)
			}
			onlyA := !sb.Contains(v)
			if onlyA != d.Contains(v) {
				t.Fatalf("Diff(%q, %q): membership of %q = %v, want %v", a, b, v, d.Contains(v), onlyA)
			}
		}
		for _, v := range b {
			if !u.Contains(v) {
				t.Fatalf("Union(%q, %q) is missing %q from b", a, b, v)
			}
			if d.Contains(v) {
				t.Fatalf("Diff(%q, %q) contains %q from b", a, b, v)
			}
		}
	})
}
