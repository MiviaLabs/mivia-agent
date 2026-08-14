// Package setutil provides a generic set backed by a hash map, plus the
// Union, Intersect, and Diff set operations as free functions. It is a leaf
// package: it imports only the standard library.
package setutil

// Set is a collection of distinct values of type T. The zero value is an
// empty set ready for use: Add lazily allocates the backing map, and Remove,
// Contains, and Len are safe on a zero value.
type Set[T comparable] struct {
	m map[T]struct{}
}

// New returns an empty Set that already contains every item. Duplicate items
// are stored once, in no particular order.
func New[T comparable](items ...T) *Set[T] {
	s := &Set[T]{}
	for _, item := range items {
		s.Add(item)
	}
	return s
}

// Add inserts v into the set. Adding a value that is already present is a
// no-op.
func (s *Set[T]) Add(v T) {
	if s.m == nil {
		s.m = make(map[T]struct{})
	}
	s.m[v] = struct{}{}
}

// Remove deletes v from the set. Removing a value that is not present is a
// no-op.
func (s *Set[T]) Remove(v T) {
	delete(s.m, v)
}

// Contains reports whether v is in the set.
func (s *Set[T]) Contains(v T) bool {
	_, ok := s.m[v]
	return ok
}

// Len returns the number of distinct values in the set.
func (s *Set[T]) Len() int {
	return len(s.m)
}

// Union returns a new set holding every value that is in a or b. Neither a
// nor b is modified.
func Union[T comparable](a, b *Set[T]) *Set[T] {
	out := New[T]()
	for v := range a.m {
		out.Add(v)
	}
	for v := range b.m {
		out.Add(v)
	}
	return out
}

// Intersect returns a new set holding every value that is in both a and b.
// Neither a nor b is modified.
func Intersect[T comparable](a, b *Set[T]) *Set[T] {
	out := New[T]()
	for v := range a.m {
		if b.Contains(v) {
			out.Add(v)
		}
	}
	return out
}

// Diff returns a new set holding every value that is in a but not in b.
// Neither a nor b is modified.
func Diff[T comparable](a, b *Set[T]) *Set[T] {
	out := New[T]()
	for v := range a.m {
		if !b.Contains(v) {
			out.Add(v)
		}
	}
	return out
}
