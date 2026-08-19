// filespanel is the cockpit's touched-files panel: a persistent,
// focusable pane over the files this session touched, fed live by the
// same event stream that drives the transcript. It is not a modal - the
// conversation stays on screen beside it (wide) or below it (narrow) -
// and not a snapshot: a file edited while the panel is open appears in
// it immediately.
//
// Wide layout (at and above uikitconfig.BreakpointWide): the chat
// column shrinks into the split's left reading pane, the list takes the
// right nav pane, and selecting a file opens its diff or source as a
// dialog over the LEFT pane only - the list stays visible beside it.
// Narrow layout: the list replaces the transcript area at full width,
// and the content dialog is full-width too, since there is no column to
// preserve.
package conversation

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// fileKind is how the session touched a file.
type fileKind string

const (
	fileEdited  fileKind = "edited"
	fileCreated fileKind = "created"
	fileDeleted fileKind = "deleted"
)

// fileEntry is one file the session touched.
type fileEntry struct {
	Path string
	Kind fileKind
	Diff uievent.Diff
}

// newEntry derives a fileEntry from a tool-end diff. Deleted is stated
// by the diff itself; a diff with no removals is a creation (hunks that
// only add carry no previous content); everything else is an edit.
func newEntry(d uievent.Diff) fileEntry {
	k := fileEdited
	switch {
	case d.Deleted:
		k = fileDeleted
	case d.Removed == 0:
		k = fileCreated
	}
	return fileEntry{Path: d.Path, Kind: k, Diff: d}
}

func (e fileEntry) rowLabel() string {
	return e.Path + "  " + string(e.Kind)
}

// panel is the touched-files pane. open says it is drawn, focused says
// its list takes the keyboard (the composer keeps it otherwise, so the
// user can type and send with the panel on screen the whole time), and
// dialog says the selected file's content is open as a dialog.
type panel struct {
	entries []fileEntry
	list    picker.Model

	// sourceView flips the content dialog between the diff (default)
	// and the full post-edit source.
	sourceView bool

	// offset windows the dialog's body; the list windows itself.
	offset int

	open    bool
	focused bool
	dialog  bool
}

// newPanel builds the panel's zero state; entries arrive live from
// handleTurnEvent.
func newPanel(t theme.Theme, tier theme.Tier) panel {
	return panel{list: picker.New(t, tier, nil)}
}

// appendLive folds one more observed diff into the entries, latest per
// path: the panel answers "what is the state of this file", not "what
// is the history". While the panel is open the list follows
// immediately, holding the cursor on the selected path - a live update
// must not move the user's cursor or wipe an in-progress filter.
// Closed, only the data folds; the list is rebuilt on the next open.
func (p *panel) appendLive(d uievent.Diff) {
	selPath := ""
	if p.open {
		if sel, ok := p.list.Selected(); ok {
			selPath = strings.Split(sel, "  ")[0]
		}
	}
	for i, e := range p.entries {
		if e.Path == d.Path {
			p.entries[i] = newEntry(d)
			p.rebindIfOpen(selPath)
			return
		}
	}
	p.entries = append(p.entries, newEntry(d))
	p.rebindIfOpen(selPath)
}

func (p *panel) rebindIfOpen(keepPath string) {
	if !p.open {
		return
	}
	names := make([]string, len(p.entries))
	for i, e := range p.entries {
		names[i] = e.rowLabel()
	}
	p.list.Rebind(names)
	// A filter may exclude the held row; Rebind has already clamped the
	// cursor to the filtered list, which is the best hold available.
	if p.list.Filter() != "" || keepPath == "" {
		return
	}
	for i, e := range p.entries {
		if e.Path == keepPath {
			p.list.MoveTo(i)
			return
		}
	}
}

// openPanel shows the panel with focus in its list, refreshing the list
// over everything observed while it was closed.
func (p *panel) openPanel() {
	p.open, p.focused, p.dialog = true, true, false
	p.rebindIfOpen("")
}

// selected returns the entry the list highlights.
func (p panel) selected() (fileEntry, bool) {
	name, ok := p.list.Selected()
	if !ok {
		return fileEntry{}, false
	}
	for _, e := range p.entries {
		if e.rowLabel() == name {
			return e, true
		}
	}
	return fileEntry{}, false
}

// contentRows is the selected file's content: its diff, or its
// post-edit source when sourceView is set.
func (p panel) contentRows(t theme.Theme, tier theme.Tier) []string {
	e, ok := p.selected()
	if !ok {
		if len(p.entries) == 0 {
			return []string{"no files touched yet"}
		}
		return nil
	}
	if p.sourceView {
		if len(e.Diff.After) == 0 {
			return []string{"source not available for this edit"}
		}
		return e.Diff.After
	}
	if lines := render.DiffLines(t, tier, e.Diff); len(lines) > 0 {
		return lines
	}
	return []string{"no changes recorded"}
}

// panelIsSplit reports the wide layout: panel open with room for two
// panes. Below the wide breakpoint the panel collapses to the list over
// the transcript area instead.
func (s Screen) panelIsSplit() bool {
	return s.panel.open && s.width >= uikitconfig.BreakpointWide
}

// chatWidth is the width the chat column (transcript, approval, status
// row, composer) renders at: the reading pane's inner width while the
// panel splits the screen, the full content width otherwise.
func (s Screen) chatWidth() int {
	if !s.panelIsSplit() {
		return contentWidth(s.width)
	}
	reading, _ := render.SplitWidths(contentWidth(s.width))
	return reading - 2
}

// transcriptHeight is the rows the transcript area holds after the
// chrome's claim - and the split pane borders' claim, when the panel is
// open wide.
func (s Screen) transcriptHeight() int {
	h := s.height - s.reservedRows()
	if s.panelIsSplit() {
		h -= 2
	}
	if h < 0 {
		return 0
	}
	return h
}

// panelBodyRows is how many content rows the dialog can show at the
// current size, for the offset's half-page steps and clamp.
func (s Screen) panelBodyRows() int {
	h := s.transcriptHeight()
	if s.panelIsSplit() {
		h = s.height - (s.topbar.Height() + 1)
	}
	return render.DialogBodyRows(h)
}

// scrollPanel moves the dialog's body window by half a page, clamped to
// the content: the tail must stay reachable.
func (s *Screen) scrollPanel(dir int) {
	s.panel.offset += dir * max(1, s.panelBodyRows()/2)
	rows := len(s.panel.contentRows(s.Theme, s.Tier))
	if max := rows - s.panelBodyRows(); max >= 0 {
		s.panel.offset = min(s.panel.offset, max)
	} else {
		s.panel.offset = 0
	}
}

// panelRows is the nav pane's content: the list, each row clipped to
// the pane's inner width and windowed to maxRows around the highlighted
// row. Clipping happens here because the pane WRAPS wide rows - it does
// not truncate them - and wrapping would add rows the frame's height
// contract cannot absorb; windowing here keeps the cursor on screen
// while preserving the picker's trailing filter indicator.
func (s Screen) panelRows(inner, maxRows int) []string {
	if len(s.panel.entries) == 0 {
		return []string{"no files touched yet"}
	}
	rows := strings.Split(s.panel.list.View(), "\n")
	for i, r := range rows {
		if ansi.StringWidth(r) > inner {
			rows[i] = ansi.Truncate(r, inner, "")
		}
	}
	filterRow := -1
	if f := s.panel.list.Filter(); f != "" && len(rows) > 0 {
		filterRow = len(rows) - 1
	}
	items := rows
	if filterRow >= 0 {
		items = rows[:filterRow]
	}
	if maxRows > 0 && len(items) > maxRows {
		start := 0
		if cur := s.panel.list.CursorRow(); cur >= maxRows {
			start = cur - maxRows + 1
		}
		items = items[start : start+maxRows]
	}
	if filterRow >= 0 {
		items = append(items, rows[filterRow])
	}
	return items
}

// dialogParts is the content dialog's title, windowed body, and hint.
func (s Screen) dialogParts() (title, body, hint string) {
	title = "files"
	if e, ok := s.panel.selected(); ok {
		title = e.Path
	}
	rows := s.panel.contentRows(s.Theme, s.Tier)
	fit := s.panelBodyRows()
	start := min(max(0, s.panel.offset), max(0, len(rows)-fit))
	end := min(start+fit, len(rows))
	return title, strings.Join(rows[start:end], "\n"), "d diff/source  any key closes"
}

// panelFrameRows draws the wide layout's panes and returns exactly
// paneH rows: the chat column in the left reading pane (or the content
// dialog over that pane, with the list still visible beside it) and the
// file list in the right nav pane.
func (s Screen) panelFrameRows() []string {
	w := contentWidth(s.width)
	paneH := max(1, s.height-(s.topbar.Height()+1)) // the top bar and its margin row
	_, navW := render.SplitWidths(w)

	var frame string
	switch {
	case s.panel.dialog && paneH >= 6:
		title, body, hint := s.dialogParts()
		frame = render.SplitDialog(s.Theme, s.Tier, w, paneH, title, body, hint,
			strings.Join(s.panelRows(navW-2, paneH-2), "\n"))
	default:
		focus := render.Left
		if s.panel.focused {
			focus = render.Right
		}
		chat := append(s.centerRows(), s.chatTailRows()...)
		if len(chat) > paneH-2 {
			chat = chat[:paneH-2]
		}
		frame = render.Split(s.Theme, s.Tier, w, paneH, focus,
			strings.Join(chat, "\n"), strings.Join(s.panelRows(navW-2, paneH-2), "\n"))
	}
	rows := strings.Split(frame, "\n")
	if len(rows) > paneH {
		rows = rows[:paneH]
	}
	for len(rows) < paneH {
		rows = append(rows, "")
	}
	return rows
}

// narrowPanelRows draws the narrow layout: the list over the transcript
// area at full width, or the content dialog over the same area, padded
// or clipped to exactly that area's height. The chrome below (approval,
// status row, composer) keeps its place.
func (s Screen) narrowPanelRows() []string {
	h := s.transcriptHeight()
	w := contentWidth(s.width)
	if s.panel.dialog && h >= 6 {
		title, body, hint := s.dialogParts()
		return overlayRows(render.Dialog(s.Theme, s.Tier, w, h, title, body, hint), h)
	}
	return overlayRows(strings.Join(s.panelRows(w-2, h), "\n"), h)
}

// chatTailRows is the chat column below the transcript area: the
// approval prompt when armed, the status row, and the composer.
func (s Screen) chatTailRows() []string {
	var rows []string
	if v := s.approval.View(); v != "" {
		rows = append(rows, strings.Split(v, "\n")...)
	}
	rows = append(rows, s.statusRow())
	rows = append(rows, strings.Split(s.composer.View(), "\n")...)
	return rows
}

// centerRows is the transcript area's content: an open picker dialog,
// the overlay, or the transcript rows.
func (s Screen) centerRows() []string {
	switch {
	case s.modelPicker != nil:
		dw, dh := s.dialogSize()
		return overlayRows(renderPickerDialog(s.Theme, s.Tier, dw, dh, "select a model", *s.modelPicker), s.transcriptHeight())
	case s.agentPicker != nil:
		dw, dh := s.dialogSize()
		return overlayRows(renderPickerDialog(s.Theme, s.Tier, dw, dh, "select an agent", *s.agentPicker), s.transcriptHeight())
	case s.overlay != "":
		return overlayRows(s.overlay, s.transcriptHeight())
	default:
		return s.transcript.Rows()
	}
}
