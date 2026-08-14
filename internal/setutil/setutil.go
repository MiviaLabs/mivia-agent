// Package setutil provides a small generic set backed by a map and the set
// operations union, intersection, and difference. It has no external
// dependencies and keeps no state beyond its elements.
package setutil

// Set is a generic set of comparable values backed by map[T]struct{}.
//
// Set is a map type, so its zero value is nil. Create a set with NewSet or
// NewSetFrom before you call Add. Contains, Remove, and Len are safe on a
// nil set.
type Set[T comparable] map[T]struct{}

// NewSet returns an empty set ready for Add.
func NewSet[T comparable]() Set[T] {
	return make(Set[T])
}

// NewSetFrom returns a set that contains items. Duplicate items collapse to
// one element.
func NewSetFrom[T comparable](items []T) Set[T] {
	s := NewSet[T]()
	for _, item := range items {
		s.Add(item)
	}
	return s
}

// Add inserts v into the set. Adding an existing value is a no-op.
func (s Set[T]) Add(v T) {
	s[v] = struct{}{}
}

// Remove deletes v from the set. Removing an absent value is a no-op.
func (s Set[T]) Remove(v T) {
	delete(s, v)
}

// Contains reports whether v is in the set.
func (s Set[T]) Contains(v T) bool {
	_, ok := s[v]
	return ok
}

// Len returns the number of elements in the set.
func (s Set[T]) Len() int {
	return len(s)
}

// Union returns a new set with every element of a and b.
func Union[T comparable](a, b Set[T]) Set[T] {
	out := NewSet[T]()
	for v := range a {
		out.Add(v)
	}
	for v := range b {
		out.Add(v)
	}
	return out
}

// Intersect returns a new set with the elements that a and b share.
func Intersect[T comparable](a, b Set[T]) Set[T] {
	out := NewSet[T]()
	for v := range a {
		if b.Contains(v) {
			out.Add(v)
		}
	}
	return out
}

// Diff returns a new set with the elements of a that are not in b.
func Diff[T comparable](a, b Set[T]) Set[T] {
	out := NewSet[T]()
	for v := range a {
		if !b.Contains(v) {
			out.Add(v)
		}
	}
	return out
}
