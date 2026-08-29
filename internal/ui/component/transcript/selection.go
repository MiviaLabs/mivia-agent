package transcript

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/select"
)

// Mouse text selection over the visible window. The owning screen
// injects the absolute rect during layout (only the screen knows the
// topbar height and gutters); the model owns the anchor/focus pair and
// paints the reverse-video highlight inside Rows(), so a single View
// pass draws both content and selection. SelectedText derives the
// copied plain text from the same rendered rows - the only faithful
// reading of "what the user sees", collapsed headers included, because
// a leader row stands in for its whole run on screen too.

// SelectionRect returns the region's current absolute screen rect, as
// injected by SetSelectionRect.
func (m Model) SelectionRect() sel.Rect { return m.selRect }

// SetSelectionRect records the region's absolute screen rect. The
// screen calls it wherever it sizes the transcript.
func (m *Model) SetSelectionRect(r sel.Rect) { m.selRect = r }

// SetSelection records the live drag selection in viewport-local cells.
func (m *Model) SetSelection(s sel.Selection) { m.selState = s }

// Selection reports the current selection, including the armed anchor.
func (m Model) Selection() sel.Selection { return m.selState }

// ClearSelection drops any selection and its highlight.
func (m *Model) ClearSelection() { m.selState = sel.Selection{} }

// HasSelection reports whether a selection is active. The screen uses
// it to keep a live drag visible across frames.
func (m Model) HasSelection() bool { return m.selState.Active }

// SelectedText returns the plain stream text between anchor and focus
// over the visible rows. It strips the highlight before cutting so the
// reverse-video spans cannot shift the cell boundaries, then re-applies
// nothing: the copy carries no styling.
func (m Model) SelectedText() string {
	if !m.selState.Active {
		return ""
	}
	from, to := m.selState.Ordered()
	rows := m.Rows()
	for i, line := range rows {
		rows[i] = stripReverse(line)
	}
	return sel.StreamSelect(rows, from, to)
}

// invalidateSelection drops a live selection because the rows under it
// moved or vanished (new blocks pushed, ring trim shifted the top, the
// viewport scrolled). A stale anchor would copy the wrong text, so the
// honest behavior is to cancel the drag.
func (m *Model) invalidateSelection() {
	m.selState = sel.Selection{}
}

// stripReverse removes the SGR 7/27 highlight markers Rows() added, so
// StreamSelect cuts cells from the un-highlighted row.
func stripReverse(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\x1b[7m", ""), "\x1b[27m", "")
}
