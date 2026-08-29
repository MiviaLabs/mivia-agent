package transcript

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
)

// Mouse text selection over the pager's visible rows. The pager owns
// its region outright: it draws the whole surface, so the rect is one
// row-window of contentWidth columns inside the gutter. Handover mode
// reports no region - there the terminal holds the mouse and its own
// scrollback selection already works.
//
// selState is a shared pointer (see Screen): the router arms the
// selection through the live stack slot, and every copy of that slot
// made afterwards must still see it when View paints. SetSelection
// therefore copy-on-writes (the armed anchor in the shared Selection
// stays untouched); ClearSelection drops the local reference so the
// next frame paints nothing.

// SelectionRect returns the pager body's absolute screen rect.
func (s Screen) SelectionRect() sel.Rect { return s.selectionRegion() }

// SetSelection records the live drag selection in viewport-local cells,
// copying first so the previously published copies keep painting the
// state they were rendered with.
func (s *Screen) SetSelection(x sel.Selection) {
	if s.selState == nil || *s.selState != x {
		v := x
		s.selState = &v
	}
}

// Selection reports the current selection, including the armed anchor.
func (s Screen) Selection() sel.Selection {
	if s.selState == nil {
		return sel.Selection{}
	}
	return *s.selState
}

// ClearSelection drops any selection and its highlight.
func (s *Screen) ClearSelection() { s.selState = nil }

// HasSelection reports whether a selection is active.
func (s Screen) HasSelection() bool { return s.selState != nil && s.selState.Active }

// SelectedText returns the plain stream text between anchor and focus
// over the visible rows. Pager rows are already plain text; styles are
// stripped defensively so a future styled row cannot leak sequences
// into the clipboard.
func (s Screen) SelectedText() string {
	if !s.HasSelection() || s.mode != modePager {
		return ""
	}
	from, to := s.selState.Ordered()
	rows := make([]string, 0, s.contentHeight())
	for i := 0; i < s.contentHeight(); i++ {
		row := s.offset + i
		if row >= len(s.rows) {
			break
		}
		rows = append(rows, ansi.Strip(s.rows[row]))
	}
	return sel.StreamSelect(rows, from, to)
}

// SelectionRegions implements sel.RegionsScreen for the router. The
// handle is a pointer into the live stack entry: deliverTop replaces
// the slot after each Update, and only that slot's state renders.
func (s *Screen) SelectionRegions() []sel.RegionEntry {
	if r := s.selectionRegion(); r.Height() > 0 && r.Width() > 0 {
		var h sel.Selectable = s
		return []sel.RegionEntry{{ID: sel.RegionPager, Handle: &h}}
	}
	return nil
}

// selectionRegion is the absolute rect of the pager's content window:
// inside the one-column gutter, above the status line, none at all
// while the surface is handed back to the terminal.
func (s Screen) selectionRegion() sel.Rect {
	if s.mode != modePager || s.width < 3 || s.height < 3 {
		return sel.Rect{}
	}
	x0 := 1
	y0 := 0
	return sel.Rect{MinX: x0, MinY: y0, MaxX: x0 + contentWidth(s.width), MaxY: y0 + s.contentHeight()}
}

// handleCopyToast notices a completed drag-copy on the status line,
// matching the conversation screen's discipline: OSC 52 gives no
// delivery confirmation, so the wording states what was attempted.
func (s *Screen) handleCopyToast(text string) {
	n := utf8.RuneCountInString(strings.ReplaceAll(text, "\n", ""))
	s.notice = fmt.Sprintf("copied %d chars", n)
}

var _ sel.Selectable = (*Screen)(nil)
