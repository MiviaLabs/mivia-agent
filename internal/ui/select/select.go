// Package sel holds the value types for component-owned mouse text
// selection. A selectable region (the transcript window, the composer
// input) owns its screen rect, its anchor/focus cells, and the plain
// text between them; the app router only drives the arm/drag/commit
// state machine across those regions. Coordinates are absolute screen
// cells (zero-based, matching tea.MouseMsg) at the boundary and
// region-local cells inside a component.
//
// This package imports only stdlib, bubbletea, and x/ansi. It sits
// below internal/ui/component/* and internal/ui/screen/* in the
// layering so both may use it without an import cycle through app.
package sel

// RegionID names one selectable region within a screen.
type RegionID string

const (
	RegionTranscript RegionID = "transcript"
	RegionComposer   RegionID = "composer"
	RegionPager      RegionID = "pager"
)

// Rect is an absolute screen rectangle, zero-based, half-open on Max:
// [MinX, MaxX) columns and [MinY, MaxY) rows - the same grid
// tea.MouseMsg reports against.
type Rect struct {
	MinX, MinY, MaxX, MaxY int
}

// Contains reports whether the screen cell (x, y) falls inside r.
func (r Rect) Contains(x, y int) bool {
	return x >= r.MinX && x < r.MaxX && y >= r.MinY && y < r.MaxY
}

// Width returns the column count of r.
func (r Rect) Width() int { return r.MaxX - r.MinX }

// Height returns the row count of r.
func (r Rect) Height() int { return r.MaxY - r.MinY }

// Cell is a (row, col) position inside a region's own grid.
type Cell struct {
	Row, Col int
}

// Selection is an anchor/focus pair in region-local cells. Anchor is
// where the press armed the selection; focus is where the drag (or
// release) last reached. Active distinguishes a live or pending
// selection from none.
type Selection struct {
	Active bool
	Anchor Cell
	Focus  Cell
}

// Ordered returns the two cells normalized so the earlier one in
// reading order comes first.
func (s Selection) Ordered() (from, to Cell) {
	a, f := s.Anchor, s.Focus
	if a.Row > f.Row || (a.Row == f.Row && a.Col > f.Col) {
		a, f = f, a
	}
	return a, f
}

// FromScreen converts an absolute screen cell to a region-local cell,
// clamping into the region so a press or release at the grid edge (and
// the spurious out-of-grid coordinates some terminals report past it)
// still lands on a real cell.
func FromScreen(r Rect, x, y int) Cell {
	c := Cell{Row: y - r.MinY, Col: x - r.MinX}
	if c.Row < 0 {
		c.Row = 0
	}
	if h := r.Height() - 1; c.Row > h {
		c.Row = h
	}
	if c.Col < 0 {
		c.Col = 0
	}
	if w := r.Width() - 1; c.Col > w {
		c.Col = w
	}
	return c
}

// Selectable is what a component with a selectable text region
// implements. The component owns its rect (injected by the owning
// screen during layout), its selection state, and the plain-text
// extraction; it also paints the highlight itself during View, so the
// router never re-renders a frame to read it back.
type Selectable interface {
	// SelectionRect returns the region's current absolute screen rect.
	SelectionRect() Rect
	// SetSelection records the live selection (region-local cells).
	SetSelection(s Selection)
	// Selection reports the current selection, including the anchor the
	// last SetSelection armed. The router reads it back instead of
	// keeping its own copy: a value-copy of the router between press
	// and motion must not lose the armed state.
	Selection() Selection
	// ClearSelection drops any selection and its highlight.
	ClearSelection()
	// SelectedText returns the plain (style-stripped) stream text
	// between anchor and focus, derived from the component's model -
	// not from a re-rendered string. Empty when no selection is active.
	SelectedText() string
}

// RegionEntry pairs a region id with the component that owns it. The
// handle is a pointer to the screen's own field (or to the stack slot's
// Selectable value): SetSelection and ClearSelection must mutate live
// state, and a value copy of a value-receiver component would mutate a
// copy instead.
type RegionEntry struct {
	ID     RegionID
	Handle *Selectable
}

// RegionsScreen is implemented by any app.Screen that offers selectable
// regions for the current frame. The router hit-tests presses against
// these and routes drag updates to the owning handle.
type RegionsScreen interface {
	SelectionRegions() []RegionEntry
}

// CopyTextMsg announces a completed copy-to-clipboard of Text so the
// visible screens can toast it on their status lines. OSC 52 gives no
// delivery confirmation, so the notice says what was attempted.
type CopyTextMsg struct {
	Text string
}
