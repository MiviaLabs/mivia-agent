package conversation

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
)

// Selectable regions for the router's drag-select (internal/ui/app/
// mouse_router.go). The screen owns the geometry - only it knows the
// gutter, topbar height, split panel, and the chrome rows under the
// transcript - so it injects each region's absolute rect at render time
// and reports the same rects here. Both must agree: they read from one
// source, the helpers below, which mirror View's assembly row by row.
//
// Two regions are selectable: the transcript window and the composer
// body (menu and border rows are not). The approval prompt, history,
// queue, blackboard, dialogs, and the nav pane carry no region in v1:
// a press there falls through to handleClick exactly as before. The
// embedded subagent-thread construction renders inside a dialog frame
// whose coordinates belong to the parent, so it reports none either.

func (s *Screen) setTranscriptRect() {
	if s.embedded {
		return
	}
	s.transcript.SetSelectionRect(s.transcriptRegion())
}

func (s *Screen) setComposerRect() {
	if s.embedded || s.hideComposer {
		return
	}
	s.composer.SetSelectionRect(s.composerRegion())
}

// SelectionRegions implements sel.RegionsScreen for the router's
// hit-testing. Rects were injected at the last render; re-injecting is
// cheap arithmetic, and it keeps a click on a freshly resized layout
// honest even if the router asks before the next View. The handles are
// pointers into this screen's fields (the receiver's addressable copy),
// so SetSelection/ClearSelection reach the state that renders.
func (s *Screen) SelectionRegions() []sel.RegionEntry {
	if s.embedded {
		return nil
	}
	s.setTranscriptRect()
	s.setComposerRect()
	var out []sel.RegionEntry
	if tr := s.transcriptRegion(); s.transcriptShown() && tr.Height() > 0 && tr.Width() > 0 {
		var h sel.Selectable = &s.transcript
		out = append(out, sel.RegionEntry{ID: sel.RegionTranscript, Handle: &h})
	}
	if !s.hideComposer {
		if cr := s.composerRegion(); cr.Height() > 0 && cr.Width() > 0 {
			var h sel.Selectable = &s.composer
			out = append(out, sel.RegionEntry{ID: sel.RegionComposer, Handle: &h})
		}
	}
	return out
}

// syncSelectionRects refreshes the components' injected rects after any
// layout change - resize, reflow, panel toggle, approval armed/cleared -
// so their own View paints the highlight at the right cells even when
// the router never asked for regions this frame. Called from Update
// (pointer receiver); a value copy simply carries the previous frame's
// rects until the next call, which the drag invalidations cover.
func (s *Screen) syncSelectionRects() {
	s.setTranscriptRect()
	s.setComposerRect()
}

// transcriptRegion is the absolute rect of the transcript area: the
// rows View draws between the topbar margin and the tail chrome, inside
// the one-column gutter, within the chat column when the panel splits.
func (s Screen) transcriptRegion() sel.Rect {
	x0, tg := s.contentOrigin()
	y0 := tg + s.topbar.Height() + 1 // topbar rows, then the margin row
	return sel.Rect{MinX: x0, MinY: y0, MaxX: x0 + s.chatWidth(), MaxY: y0 + s.transcriptHeight()}
}

// composerRegion is the absolute rect of the composer's text body only:
// padding rows and completion-menu rows are excluded, and the columns run
// across the inner width where the textarea actually draws. The composer
// draws no border (see composer.Model.View); its padding rows and columns
// occupy exactly the cells the border used to, so the geometry is the same.
func (s Screen) composerRegion() sel.Rect {
	x0, tg := s.contentOrigin()
	menuRows := s.composer.MenuRows()
	padRows := 0
	if s.composer.Padded() {
		padRows = 2 // top + bottom padding row
	}
	// bodyRows is never below 1: composer.Height() already adds the same
	// pad/menu terms subtracted here (menuRows mirrors MenuRows(), padRows
	// mirrors Padded()'s own two rows), so this always reduces to the
	// textarea's own row count, which composer.Height() clamps to at least 1.
	bodyRows := s.composer.Height() - menuRows - padRows
	// The status row sits at the screen bottom; the composer block ends
	// just above it. InputRowFromBottom counts from the status row up to
	// the LAST input row, so the first body row sits height-1 above it.
	lastInputRow := s.height - tg - s.composer.InputRowFromBottom()
	firstBodyRow := lastInputRow - (bodyRows - 1)
	colOffset := 1 + s.composer.InputColumnOffset() // gutter, then the bar's left padding
	x1 := x0 + colOffset
	w := s.chatWidth() - colOffset - 2 // right border + padding inset
	if w < 1 {
		return sel.Rect{} // the body cannot draw a single cell: no region
	}
	return sel.Rect{MinX: x1, MinY: firstBodyRow, MaxX: x1 + w, MaxY: firstBodyRow + bodyRows}
}

// contentOrigin is the top-left cell of the content area: column 1 after
// the left gutter, plus the blank top row when the gutter applies. It
// mirrors gutter()'s exact condition: the whole gutter (side columns and
// blank rows) applies unless both dimensions are under three; below
// three in either dimension that part of the frame gives way.
func (s Screen) contentOrigin() (x0, topGutter int) {
	x0, topGutter = 1, 1
	if s.width > 0 && s.width < 3 && s.height > 0 && s.height < 3 {
		x0, topGutter = 0, 0 // no gutter at all on a tiny surface
		return x0, topGutter
	}
	if s.width < 3 {
		x0 = 0 // no side columns to frame
	}
	if s.height < 3 {
		topGutter = 0 // no blank rows to frame
	}
	if s.embedded {
		topGutter = 0
	}
	return x0, topGutter
}

// handleCopyToast notices a completed drag-copy on the status line. The
// router emits this after tea.SetClipboard; OSC 52 gives no delivery
// confirmation, so the wording states what was attempted, matching the
// "copied the block" notice discipline (keys.go IDCopyBlock).
func (s *Screen) handleCopyToast(text string) {
	n := utf8.RuneCountInString(strings.ReplaceAll(text, "\n", ""))
	s.statusline.Notice(fmt.Sprintf("copied %d chars", n))
}

// compile-time proof both components satisfy the router's contract.
var (
	_ sel.Selectable = (*transcript.Model)(nil)
	_ sel.Selectable = (*composer.Model)(nil)
)
