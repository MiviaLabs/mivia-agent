package sel

import "testing"

func TestOrderedEqualCellsStable(t *testing.T) {
	s := Selection{Anchor: Cell{Row: 1, Col: 3}, Focus: Cell{Row: 1, Col: 3}}
	from, to := s.Ordered()
	if from != to || from != (Cell{Row: 1, Col: 3}) {
		t.Fatalf("equal cells must stay equal: %+v %+v", from, to)
	}
	// Same row, focus col greater: no swap (a >= mutant would swap).
	s = Selection{Anchor: Cell{Row: 0, Col: 1}, Focus: Cell{Row: 0, Col: 4}}
	from, to = s.Ordered()
	if from != (Cell{Row: 0, Col: 1}) || to != (Cell{Row: 0, Col: 4}) {
		t.Fatalf("ascending same-row pair must not swap: %+v %+v", from, to)
	}
}

func TestFromScreenExactEdgesNoClamp(t *testing.T) {
	r := Rect{MinX: 2, MinY: 3, MaxX: 6, MaxY: 7}
	if c := FromScreen(r, 2, 3); c != (Cell{Row: 0, Col: 0}) {
		t.Fatalf("min corner maps to origin: %+v", c)
	}
	if c := FromScreen(r, 5, 6); c != (Cell{Row: 3, Col: 3}) {
		t.Fatalf("max-minus-one maps to far corner: %+v", c)
	}
}

func TestOrderedReversalStrictlyAfterOnly(t *testing.T) {
	// a.Row == f.Row && a.Col == f.Col: no swap; equal cells stay put.
	s := Selection{Anchor: Cell{Row: 0, Col: 3}, Focus: Cell{Row: 0, Col: 3}}
	from, to := s.Ordered()
	if from != (Cell{Row: 0, Col: 3}) || to != (Cell{Row: 0, Col: 3}) {
		t.Fatalf("equal cells must not swap: %+v %+v", from, to)
	}
	// Multi-row reversed normalizes.
	s = Selection{Anchor: Cell{Row: 2, Col: 1}, Focus: Cell{Row: 0, Col: 4}}
	from, to = s.Ordered()
	if from != (Cell{Row: 0, Col: 4}) || to != (Cell{Row: 2, Col: 1}) {
		t.Fatalf("multi-row reversal failed: %+v %+v", from, to)
	}
}

func TestFromScreenExactEdgesNoClampB(t *testing.T) {
	r := Rect{MinX: 2, MinY: 3, MaxX: 6, MaxY: 7}
	// One inside each edge maps without clamping.
	if c := FromScreen(r, 2, 3); c != (Cell{Row: 0, Col: 0}) {
		t.Fatalf("min corner: %+v", c)
	}
	if c := FromScreen(r, 5, 6); c != (Cell{Row: 3, Col: 3}) {
		t.Fatalf("max-minus-one corner: %+v", c)
	}
	// Exactly one outside each edge clamps by one.
	if c := FromScreen(r, 1, 3); c != (Cell{Row: 0, Col: 0}) {
		t.Fatalf("left clamp: %+v", c)
	}
	if c := FromScreen(r, 6, 7); c != (Cell{Row: 3, Col: 3}) {
		t.Fatalf("right/bottom clamp: %+v", c)
	}
}
