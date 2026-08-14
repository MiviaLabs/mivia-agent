package setutil

import "testing"

// TestNewEmpty checks that a set created with no items is empty.
func TestNewEmpty(t *testing.T) {
	s := New[int]()
	if s.Len() != 0 {
		t.Errorf("New() Len = %d, want 0", s.Len())
	}
	if s.Contains(1) {
		t.Error("New() Contains(1) = true, want false")
	}
}

// TestZeroValue checks that the zero-value set is usable without New.
func TestZeroValue(t *testing.T) {
	var s Set[int]
	if s.Len() != 0 {
		t.Errorf("zero value Len = %d, want 0", s.Len())
	}
	if s.Contains(1) {
		t.Error("zero value Contains(1) = true, want false")
	}
	s.Remove(1) // must not panic on a nil backing map
	s.Add(1)
	if !s.Contains(1) {
		t.Error("zero value Add(1) did not store the value")
	}
	if s.Len() != 1 {
		t.Errorf("zero value Len after Add = %d, want 1", s.Len())
	}
}

// TestAddAndContains checks Add stores distinct values and duplicate adds are
// no-ops.
func TestAddAndContains(t *testing.T) {
	s := New[int](1, 2)
	s.Add(3)
	s.Add(2) // duplicate
	if s.Len() != 3 {
		t.Errorf("Len = %d, want 3", s.Len())
	}
	for _, v := range []int{1, 2, 3} {
		if !s.Contains(v) {
			t.Errorf("Contains(%d) = false, want true", v)
		}
	}
	if s.Contains(4) {
		t.Error("Contains(4) = true, want false")
	}
}

// TestRemove checks Remove deletes a stored value, ignores absent values, and
// shrinks Len.
func TestRemove(t *testing.T) {
	s := New[int](1, 2, 3)
	s.Remove(2)
	if s.Contains(2) {
		t.Error("Contains(2) after Remove = true, want false")
	}
	if s.Len() != 2 {
		t.Errorf("Len after Remove = %d, want 2", s.Len())
	}
	s.Remove(99) // absent: no-op
	if s.Len() != 2 {
		t.Errorf("Len after removing absent = %d, want 2", s.Len())
	}
	s.Remove(1)
	s.Remove(3)
	if s.Len() != 0 {
		t.Errorf("Len after removing all = %d, want 0", s.Len())
	}
}

// TestNewWithItems checks the constructor preloads and de-duplicates items.
func TestNewWithItems(t *testing.T) {
	s := New[int](4, 4, 5)
	if s.Len() != 2 {
		t.Errorf("Len = %d, want 2", s.Len())
	}
	if !s.Contains(4) || !s.Contains(5) {
		t.Error("New(4, 4, 5) is missing a stored value")
	}
}

// TestUnion checks the union of two sets, including empty-set operands.
func TestUnion(t *testing.T) {
	a := New[int](1, 2)
	b := New[int](2, 3)
	got := Union(a, b)
	want := New[int](1, 2, 3)
	if got.Len() != want.Len() || !got.Contains(1) || !got.Contains(2) || !got.Contains(3) {
		t.Errorf("Union([1 2], [2 3]) = %v, want {1 2 3}", setElements(got))
	}

	empty := New[int]()
	got = Union(empty, a)
	if got.Len() != a.Len() || !got.Contains(1) || !got.Contains(2) {
		t.Errorf("Union(empty, [1 2]) = %v, want {1 2}", setElements(got))
	}
	got = Union(a, empty)
	if got.Len() != a.Len() || !got.Contains(1) || !got.Contains(2) {
		t.Errorf("Union([1 2], empty) = %v, want {1 2}", setElements(got))
	}
	if got := Union(empty, empty); got.Len() != 0 {
		t.Errorf("Union(empty, empty) Len = %d, want 0", got.Len())
	}
}

// TestIntersect checks the intersection of two sets, including empty and
// disjoint operands.
func TestIntersect(t *testing.T) {
	a := New[int](1, 2, 3)
	b := New[int](2, 3, 4)
	got := Intersect(a, b)
	if got.Len() != 2 || !got.Contains(2) || !got.Contains(3) {
		t.Errorf("Intersect([1 2 3], [2 3 4]) = %v, want {2 3}", setElements(got))
	}

	disjoint := New[int](9)
	got = Intersect(a, disjoint)
	if got.Len() != 0 {
		t.Errorf("Intersect with disjoint set Len = %d, want 0", got.Len())
	}
	got = Intersect(a, New[int]())
	if got.Len() != 0 {
		t.Errorf("Intersect with empty set Len = %d, want 0", got.Len())
	}
}

// TestDiff checks the set difference a minus b, including empty operands.
func TestDiff(t *testing.T) {
	a := New[int](1, 2, 3, 4)
	b := New[int](2, 4)
	got := Diff(a, b)
	if got.Len() != 2 || !got.Contains(1) || !got.Contains(3) {
		t.Errorf("Diff([1 2 3 4], [2 4]) = %v, want {1 3}", setElements(got))
	}

	got = Diff(a, New[int]())
	if got.Len() != a.Len() {
		t.Errorf("Diff(a, empty) Len = %d, want %d", got.Len(), a.Len())
	}
	got = Diff(New[int](), a)
	if got.Len() != 0 {
		t.Errorf("Diff(empty, a) Len = %d, want 0", got.Len())
	}
	got = Diff(a, a)
	if got.Len() != 0 {
		t.Errorf("Diff(a, a) Len = %d, want 0", got.Len())
	}
}

// TestSetOperationsDoNotMutateInputs checks that Union, Intersect, and Diff
// leave both operands unchanged.
func TestSetOperationsDoNotMutateInputs(t *testing.T) {
	a := New[int](1, 2)
	b := New[int](2, 3)
	Union(a, b)
	Intersect(a, b)
	Diff(a, b)
	if a.Len() != 2 || !a.Contains(1) || !a.Contains(2) {
		t.Errorf("a mutated by an operation: %v", setElements(a))
	}
	if b.Len() != 2 || !b.Contains(2) || !b.Contains(3) {
		t.Errorf("b mutated by an operation: %v", setElements(b))
	}
}

// TestSetStrings checks the generic set behavior on a string element type.
func TestSetStrings(t *testing.T) {
	s := New[string]("alpha", "beta")
	s.Add("gamma")
	s.Remove("beta")
	if s.Len() != 2 || !s.Contains("alpha") || !s.Contains("gamma") {
		t.Errorf("string set = %v, want {alpha gamma}", setElements(s))
	}
	u := Union(s, New[string]("delta"))
	if u.Len() != 3 || !u.Contains("delta") {
		t.Errorf("string union = %v, want {alpha gamma delta}", setElements(u))
	}
}

// setElements returns the values of s as a slice for readable failure output.
func setElements[T comparable](s *Set[T]) []T {
	var out []T
	if s == nil {
		return out
	}
	for v := range s.m {
		out = append(out, v)
	}
	return out
}
