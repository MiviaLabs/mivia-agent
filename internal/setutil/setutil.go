// Package setutil provides a small generic set built on a map plus the three
// classic set operations Union, Intersect, and Diff. It has no dependencies
// outside the standard library.
package setutil

// Set is a generic set of comparable elements backed by a map from each
// element to an empty struct. The zero value is ready to use: Add allocates
// the backing map lazily, and the read methods treat a nil map as empty.
type Set[T comparable] struct {
	m map[T]struct{}
}

// Add inserts v into the set. Adding an element that is already present is a
// no-op. The zero value is usable: Add allocates the backing map on demand.
func (s *Set[T]) Add(v T) {
	if s.m == nil {
		s.m = make(map[T]struct{})
	}
	s.m[v] = struct{}{}
}

// Remove deletes v from the set. Removing an absent element is a no-op.
func (s Set[T]) Remove(v T) {
	delete(s.m, v)
}

// Contains reports whether v is in the set.
func (s Set[T]) Contains(v T) bool {
	_, ok := s.m[v]
	return ok
}

// Len returns the number of elements in the set.
func (s Set[T]) Len() int {
	return len(s.m)
}

// Union returns a new set with every element that is in a or b.
func Union[T comparable](a, b Set[T]) Set[T] {
	out := Set[T]{m: make(map[T]struct{}, a.Len()+b.Len())}
	for v := range a.m {
		out.m[v] = struct{}{}
	}
	for v := range b.m {
		out.m[v] = struct{}{}
	}
	return out
}

// Intersect returns a new set with every element that is in both a and b.
// It iterates the smaller input, so the work is proportional to it.
func Intersect[T comparable](a, b Set[T]) Set[T] {
	if a.Len() > b.Len() {
		a, b = b, a
	}
	out := Set[T]{m: make(map[T]struct{})}
	for v := range a.m {
		if _, ok := b.m[v]; ok {
			out.m[v] = struct{}{}
		}
	}
	return out
}

// Diff returns a new set with every element that is in a but not in b.
func Diff[T comparable](a, b Set[T]) Set[T] {
	out := Set[T]{m: make(map[T]struct{}, a.Len())}
	for v := range a.m {
		if _, ok := b.m[v]; !ok {
			out.m[v] = struct{}{}
		}
	}
	return out
}
