// filespanel is the cockpit's activity sidebar: a persistent,
// focusable pane over what the session is DOING - the files it changed
// and the subagents it dispatched - fed live by the same event stream
// that drives the transcript. It is not a modal - the conversation
// stays on screen beside it (wide) or below it (narrow) - and not a
// snapshot: activity while the panel is open appears in it immediately.
//
// The sections are categories of thing, not statuses: "files changed"
// holds every touched file (its kind - + created, ~ edited, -
// deleted - is a per-row glyph), "subagents" holds dispatched agents
// with their status. Empty sections keep their headers, so the shape of
// the session's work stays visible.
//
// Wide layout (at and above uikitconfig.BreakpointWide): the chat
// column shrinks to the split's reading width, the list takes the nav
// sidebar on the right, and ONE vertical rule on the sidebar's left
// edge is the only frame the split draws (the rule carries the
// focus-border colour). Selecting a file opens its diff or source as a
// dialog over the chat column only - the list stays visible beside it.
// Narrow layout: the list replaces the transcript area at full width,
// and the content dialog is full-width too, since there is no column to
// preserve.
package conversation

import (
	"strconv"
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

// splitPath divides a slash path into its base name and the directory
// above it, for the panel row's name + dimmed-directory form.
func splitPath(p string) (name, dir string) {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:], p[:i]
	}
	return p, ""
}

func (e fileEntry) rowLabel() string {
	name, dir := splitPath(e.Path)
	if dir == "" {
		return name
	}
	return name + "  " + dir
}

// subagentRow is one dispatched subagent the session is tracking, fed
// by the subagent-progress events (ToolOutputBody.Progress).
type subagentRow struct {
	ID     string
	Status string
	Step   int
	Total  int
	Log    []string
}

func (a subagentRow) rowLabel() string {
	label := a.ID + "  " + a.Status
	if a.Total > 0 {
		label += " " + strconv.Itoa(a.Step) + "/" + strconv.Itoa(a.Total)
	}
	return label
}

// panel is the activity pane. open says it is drawn, focused says its
// list takes the keyboard (the composer keeps it otherwise, so the user
// can type and send with the panel on screen the whole time), and
// dialog says the selected entry's content is open as a dialog.
// dialogAgent names the subagent whose thread (or step-log fallback)
// that dialog shows; empty means a file's diff or source.
type panel struct {
	entries     []fileEntry
	agents      []subagentRow
	list        picker.Model
	dialogAgent string

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
// immediately, holding the cursor on the selected row - a live update
// must not move the user's cursor or wipe an in-progress filter.
// Closed, only the data folds; the list is rebuilt on the next open.
func (p *panel) appendLive(d uievent.Diff) {
	for i, e := range p.entries {
		if e.Path == d.Path {
			p.entries[i] = newEntry(d)
			p.rebindIfOpen()
			return
		}
	}
	p.entries = append(p.entries, newEntry(d))
	p.rebindIfOpen()
}

// rowLabels is the selectable list in render order: files, then
// subagents. The picker's cursor indexes this list; panelRows draws the
// same order, so the marked row is always the row the next key acts on.
func (p panel) rowLabels() []string {
	names := make([]string, 0, len(p.entries)+len(p.agents))
	for _, e := range p.entries {
		names = append(names, e.rowLabel())
	}
	for _, a := range p.agents {
		names = append(names, a.rowLabel())
	}
	return names
}

// selectionKey names the selected row by what it IS (a file's path, a
// subagent's id) rather than by its render label, which changes as
// statuses tick - so a rebind can hold the same row across a label
// change.
func (p panel) selectionKey() string {
	label, ok := p.list.Selected()
	if !ok {
		return ""
	}
	for _, e := range p.entries {
		if e.rowLabel() == label {
			return "f:" + e.Path
		}
	}
	for _, a := range p.agents {
		if a.rowLabel() == label {
			return "a:" + a.ID
		}
	}
	return ""
}

func (p *panel) rebindIfOpen() {
	if !p.open {
		return
	}
	keep := p.selectionKey()
	p.list.Rebind(p.rowLabels())
	if keep == "" || p.list.Filter() != "" {
		// A filter may exclude the held row; Rebind has already clamped
		// the cursor to the filtered list, which is the best hold
		// available.
		return
	}
	for i, e := range p.entries {
		if keep == "f:"+e.Path {
			p.list.MoveTo(i)
			return
		}
	}
	for i, a := range p.agents {
		if keep == "a:"+a.ID {
			p.list.MoveTo(len(p.entries) + i)
			return
		}
	}
}

// selectedAgent returns the subagent the list highlights, if any: the
// cursor walks files and subagents alike.
func (p panel) selectedAgent() (subagentRow, bool) {
	label, ok := p.list.Selected()
	if !ok {
		return subagentRow{}, false
	}
	for _, a := range p.agents {
		if a.rowLabel() == label {
			return a, true
		}
	}
	return subagentRow{}, false
}

// observeAgent folds one subagent-progress update into the agents
// section, latest per subagent - the same fold files use. The step log
// rides along: it is the fallback content when no thread Conversation
// exists for the call.
func (p *panel) observeAgent(id string, pr *uievent.Progress) {
	row := subagentRow{ID: id, Status: pr.Status, Step: pr.Step, Total: pr.TotalSteps, Log: pr.Log}
	for i, a := range p.agents {
		if a.ID == id {
			row.Log = append(row.Log, a.Log...)
			p.agents[i] = row
			p.rebindIfOpen()
			return
		}
	}
	p.agents = append(p.agents, row)
	p.rebindIfOpen()
}

// openPanel shows the panel with focus in its list, refreshing the list
// over everything observed while it was closed.
func (p *panel) openPanel() {
	p.open, p.focused, p.dialog, p.dialogAgent = true, true, false, ""
	p.rebindIfOpen()
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
// row, composer) renders at: the reading pane's width while the panel
// splits the screen, the full content width otherwise.
func (s Screen) chatWidth() int {
	if !s.panelIsSplit() {
		return contentWidth(s.width)
	}
	reading, _ := render.SplitWidths(contentWidth(s.width))
	return reading
}

// transcriptHeight is the rows the transcript area holds after the
// chrome's claim. The split draws no horizontal frame, so the claim is
// the same with the panel open as without it.
func (s Screen) transcriptHeight() int {
	h := s.height - s.reservedRows()
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
// the content: the tail must stay reachable and the offset must never
// go negative (a negative window reads as dead scroll keys).
func (s *Screen) scrollPanel(dir int) {
	s.panel.offset += dir * max(1, s.panelBodyRows()/2)
	rows := len(s.panel.contentRows(s.Theme, s.Tier))
	if max := rows - s.panelBodyRows(); max >= 0 {
		s.panel.offset = min(s.panel.offset, max)
	} else {
		s.panel.offset = 0
	}
	if s.panel.offset < 0 {
		s.panel.offset = 0
	}
}

// panelDialogFits reports whether the content dialog has room to draw
// at the current size. Enter is gated on it: a dialog flag set with no
// dialog drawn would swallow keys and clicks aimed at what IS drawn.
func (s Screen) panelDialogFits() bool {
	if s.panelIsSplit() {
		return s.height-(s.topbar.Height()+1) >= 6
	}
	return s.transcriptHeight() >= 6
}

// panelRows is the sidebar's content: sections by CATEGORY of thing -
// the files the session changed, the subagents it dispatched - each
// headed by its name and count (an empty section keeps its header, so
// the shape of the session's work stays visible). File rows name the
// file with its directory dimmed behind it and a kind glyph in front
// (+ created, ~ edited, - deleted); subagent rows state id, status, and
// step. The selected file row carries the picker's selection highlight;
// rows are clipped to inner columns and windowed to maxRows around that
// row.
// panelFilterEntries applies the picker's substring filter to the panel's
// file and subagent rows. The picker's visible set is this same filter, and
// its cursor indexes that set, so the rendered rows must agree with both.
func (s Screen) panelFilterEntries(needle string) ([]fileEntry, []subagentRow) {
	visible := make([]fileEntry, 0, len(s.panel.entries))
	for _, e := range s.panel.entries {
		if needle == "" || strings.Contains(strings.ToLower(e.rowLabel()), needle) {
			visible = append(visible, e)
		}
	}
	agents := make([]subagentRow, 0, len(s.panel.agents))
	for _, a := range s.panel.agents {
		if needle == "" || strings.Contains(strings.ToLower(a.rowLabel()), needle) {
			agents = append(agents, a)
		}
	}
	return visible, agents
}

// panelWindowRows clips rows to a window around the selection, reserving a
// row for the filter indicator when one will be appended: the indicator is
// the only on-screen explanation of a shortened list, so it must never be
// the row the windowing clips.
func panelWindowRows(rows []string, selRow, maxRows int, filterActive bool) []string {
	limit := maxRows
	if filterActive && limit > 1 {
		limit--
	}
	if limit > 0 && len(rows) > limit {
		start := 0
		if selRow >= limit {
			start = selRow - limit + 1
		}
		if start > len(rows)-limit {
			start = len(rows) - limit
		}
		rows = rows[start : start+limit]
	}
	return rows
}

// clipRowsToWidth clips styled rows by display width: the sidebar's blocks
// pad and clip, they never re-wrap, so a wide path is cut here.
func clipRowsToWidth(rows []string, inner int) []string {
	for i, r := range rows {
		if ansi.StringWidth(r) > inner {
			rows[i] = ansi.Truncate(r, inner, "")
		}
	}
	return rows
}

func (s Screen) panelFileRow(e fileEntry, selLabel string, marked bool) string {
	name, dir := splitPath(e.Path)
	glyph := map[fileKind]string{fileCreated: "+", fileEdited: "~", fileDeleted: "-"}[e.Kind]
	prefix, style := "  ", render.Role(s.Theme, s.Tier, theme.RoleFG)
	if marked && e.rowLabel() == selLabel {
		prefix = "> "
		style = render.WithBg(style, s.Theme, s.Tier, theme.RoleBGSelection)
	}
	row := style.Render(prefix + glyph + " " + name)
	if e.Diff.Added > 0 || e.Diff.Removed > 0 {
		border := render.Role(s.Theme, s.Tier, theme.RoleBorder)
		var stat string
		if e.Diff.Added > 0 && e.Diff.Removed > 0 {
			add := render.Role(s.Theme, s.Tier, theme.RoleDiffAddFG).Render("+" + strconv.Itoa(e.Diff.Added))
			del := render.Role(s.Theme, s.Tier, theme.RoleDiffDelFG).Render("-" + strconv.Itoa(e.Diff.Removed))
			stat = " " + border.Render("[") + add + border.Render("|") + del + border.Render("]")
		} else if e.Diff.Added > 0 {
			add := render.Role(s.Theme, s.Tier, theme.RoleDiffAddFG).Render("+" + strconv.Itoa(e.Diff.Added))
			stat = " " + border.Render("[") + add + border.Render("]")
		} else {
			del := render.Role(s.Theme, s.Tier, theme.RoleDiffDelFG).Render("-" + strconv.Itoa(e.Diff.Removed))
			stat = " " + border.Render("[") + del + border.Render("]")
		}
		row += stat
	}
	if dir != "" {
		row += render.Role(s.Theme, s.Tier, theme.RoleFGSubtle).Render("  " + dir)
	}
	return row
}

func (s Screen) panelAgentRow(a subagentRow, selLabel string, marked bool) string {
	prefix := "  · "
	if marked && a.rowLabel() == selLabel {
		prefix = "> · "
	}
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	fg := render.Role(s.Theme, s.Tier, theme.RoleFG)
	border := render.Role(s.Theme, s.Tier, theme.RoleBorder)
	var statusBadge string
	if a.Status != "" {
		role := theme.RoleInfo
		if a.Status == "completed" || a.Status == "done" {
			role = theme.RoleSuccess
		} else if a.Status == "failed" || a.Status == "error" {
			role = theme.RoleDanger
		} else if a.Status == "thinking" {
			role = theme.RoleWarning
		}
		statusStyle := render.Role(s.Theme, s.Tier, role)
		statusBadge = " " + border.Render("[") + statusStyle.Render(a.Status) + border.Render("]")
	}
	var stepBadge string
	if a.Total > 0 {
		stepBadge = " " + border.Render("[") + subtle.Render(strconv.Itoa(a.Step)+"/"+strconv.Itoa(a.Total)) + border.Render("]")
	}
	return subtle.Render(prefix) + fg.Render(a.ID) + statusBadge + stepBadge
}

func (s Screen) panelRows(inner, maxRows int) []string {
	needle := strings.ToLower(s.panel.list.Filter())
	visible, agents := s.panelFilterEntries(needle)

	selLabel, _ := s.panel.list.Selected()
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	marked := s.panel.focused

	var rows []string
	selRow := -1
	rows = append(rows, subtle.Render("files changed ("+strconv.Itoa(len(visible))+")"))
	for _, e := range visible {
		if e.rowLabel() == selLabel {
			selRow = len(rows)
		}
		rows = append(rows, s.panelFileRow(e, selLabel, marked))
	}
	rows = append(rows, subtle.Render("subagents ("+strconv.Itoa(len(agents))+")"))
	for _, a := range agents {
		rows = append(rows, s.panelAgentRow(a, selLabel, marked))
	}

	rows = panelWindowRows(rows, selRow, maxRows, needle != "")
	if needle != "" {
		rows = append(rows, subtle.Render("/"+s.panel.list.Filter()))
	}
	return clipRowsToWidth(rows, inner)
}

// dialogParts is the content dialog's title, body, and hint. A
// subagent entry with a live thread renders the embedded conversation
// Screen, sized to the exact body area Dialog gives it; one without
// falls back to the step log the progress events carried. File entries
// show their windowed diff or source as before.
func (s Screen) dialogParts() (title, body, hint string) {
	if s.panel.dialogAgent != "" {
		if s.thread != nil && s.threadID == s.panel.dialogAgent {
			// Size the embedded screen to the EXACT body area the
			// Dialog frame gives this body: any wider and the clip
			// cuts the composer's edge, any taller and it drops rows
			// off the bottom. panelBodyRows IS DialogBodyRows for the
			// dialog height the current mode draws.
			frameW := contentWidth(s.width)
			if s.panelIsSplit() {
				frameW, _ = render.SplitWidths(frameW)
			}
			s.thread.setSurface(render.DialogBodyWidth(frameW), s.panelBodyRows())
			return s.panel.dialogAgent, s.thread.View(), "esc close"
		}
		for _, a := range s.panel.agents {
			if a.ID != s.panel.dialogAgent {
				continue
			}
			rows := append([]string{a.ID + "  " + a.Status}, a.Log...)
			return a.ID, strings.Join(rows, "\n"), "any key closes"
		}
		return s.panel.dialogAgent, "", "any key closes"
	}
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
	case s.panel.dialog && s.panelDialogFits():
		title, body, hint := s.dialogParts()
		// The list keeps keyboard focus under its dialog ("any key
		// closes"), so the rule keeps the focused colour.
		frame = render.SplitDialog(s.Theme, s.Tier, w, paneH, s.panel.focused, title, body, hint,
			strings.Join(s.panelRows(navW-1, paneH), "\n"))
	default:
		focus := render.Left
		if s.panel.focused {
			focus = render.Right
		}
		chat := append(s.centerRows(), s.chatTailRows()...)
		if len(chat) > paneH {
			chat = chat[:paneH]
		}
		frame = render.Split(s.Theme, s.Tier, w, paneH, focus,
			strings.Join(chat, "\n"), strings.Join(s.panelRows(navW-1, paneH), "\n"))
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
	if s.panel.dialog && s.panelDialogFits() {
		title, body, hint := s.dialogParts()
		return overlayRows(render.Dialog(s.Theme, s.Tier, w, h, title, body, hint), h)
	}
	return overlayRows(strings.Join(s.panelRows(w, h), "\n"), h)
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
	case s.sessionPicker != nil:
		dw, dh := s.dialogSize()
		return overlayRows(renderSessionPickerDialog(s.Theme, s.Tier, dw, dh, *s.sessionPicker, s.now()), s.transcriptHeight())
	case s.overlay != "":
		return overlayRows(s.overlay, s.transcriptHeight())
	case !s.embedded && s.transcript.Empty():
		return s.welcome.Rows(s.chatWidth(), s.transcriptHeight())
	default:
		return s.transcript.Rows()
	}
}
