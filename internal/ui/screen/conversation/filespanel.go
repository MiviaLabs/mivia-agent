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
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// fileKind is how the session touched a file.
type fileKind string

const (
	statusPending     = "pending"
	statusRunning     = "running"
	statusThinking    = "thinking"
	statusCompleted   = "completed"
	statusFailed      = "failed"
	statusCancelled   = "cancelled"
	statusInterrupted = "interrupted"
)

func isTerminalStatus(status string) bool {
	switch status {
	// timed_out is a subagent done-event status (agent.Event.Status): a
	// timed-out run is over and its row must settle, not keep spinning.
	case "completed", "done", "failed", "error", "interrupted", "cancelled", "canceled", "timed_out":
		return true
	default:
		return false
	}
}

func isNonTerminalStatus(status string) bool {
	return !isTerminalStatus(status)
}

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
	d.Hunks = slices.Clone(d.Hunks)
	d.After = slices.Clone(d.After)
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
//
// LastProgress is the wall-clock time of the row's last forward motion:
// a heartbeat with a CHANGED step count, any non-heartbeat progress, or
// any tool event. Heartbeats whose step count is frozen are liveness, not
// motion, and leave it alone - see progressAdvances and agent_stall.go's
// displayStatus, which derives the "stalled" badge from it at render time.
type subagentRow struct {
	ID        string
	Name      string
	Status    string
	Step      int
	Total     int
	ToolCalls int
	Log       []string

	LastProgress time.Time
	// StartedAt anchors the sidebar's live "Elapsed" reading (computed at
	// render time, not carried by the 30s heartbeat cadence - see
	// panelAgentRow). Stamped fresh on every observeAgentStart, including a
	// reused id: a stale StartedAt would report the elapsed time of the run
	// that already ended, not the new one.
	StartedAt time.Time
}

func (a subagentRow) displayName() string {
	if a.Name != "" {
		return a.Name
	}
	if idx := strings.Index(a.ID, ":"); idx >= 0 && idx+1 < len(a.ID) {
		return a.ID[idx+1:]
	}
	return a.ID
}

func (a subagentRow) rowLabel() string {
	label := a.displayName() + "  " + a.Status
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

	// dispatchGroups maps a dispatch_tasks call's own tool-call id to the
	// per-task subagent row ids it fanned out into (see
	// observeAgentGroupStart). Live per-task thread registration
	// (uiadapter.SubagentThreads.HandleEvent) keys only by each nested
	// subagent's own Origin.TaskID, never by the outer dispatch_tasks
	// call's id, so the panel must track one row per dispatched task -
	// not one aggregate row for the whole call - for openThread and the
	// top-bar agent count to see every dispatched subagent.
	dispatchGroups map[string][]string

	// sourceView flips the content dialog between the diff (default)
	// and the full post-edit source.
	sourceView bool

	// stallThreshold is the no-forward-motion window after which a
	// non-terminal subagent row renders "stalled" (see agent_stall.go).
	// Seeded from uiStallThreshold; tests shrink it. 0 or less turns the
	// derivation off.
	stallThreshold time.Duration

	// offset windows the dialog's body; the list windows itself.
	offset int

	open    bool
	focused bool
	dialog  bool

	// Per-section fold state. During a long run the files and subagents
	// lists grow without bound and push each other off the pane; folding
	// one is how a reader keeps the other in view. The context section
	// folds to its header for the same reason.
	contextCollapsed bool
	filesCollapsed   bool
	agentsCollapsed  bool

	// selKey names the row the cursor was last MOVED to, captured at the
	// moment of the move.
	//
	// A rebind cannot work this out for itself. Live mutations append
	// rows and then rebind, so by the time the rebind runs the cursor
	// index already points into a list that changed underneath it: a file
	// arriving above the subagents header made the header's index name a
	// file instead, and the "restore" faithfully restored the wrong row.
	// Capturing at the move is the only point where index and model agree.
	selKey string
}

// newPanel builds the panel's zero state; entries arrive live from
// handleTurnEvent.
func newPanel(t theme.Theme, tier theme.Tier) panel {
	return panel{
		list:           picker.New(t, tier, nil),
		dispatchGroups: map[string][]string{},
		stallThreshold: uiStallThreshold,
	}
}

// appendLive folds one more observed diff into the entries, latest per
// path: the panel answers "what is the state of this file", not "what
// is the history". While the panel is open the list follows
// immediately, holding the cursor on the selected row - a live update
// must not move the user's cursor or wipe an in-progress filter.
// Closed, only the data folds; the list is rebuilt on the next open.
func (p *panel) appendLive(d uievent.Diff) {
	entry := newEntry(d)
	p.entries = slices.Clone(p.entries)
	for i, e := range p.entries {
		if e.Path == d.Path {
			p.entries[i] = entry
			p.rebindIfOpen()
			return
		}
	}
	p.entries = append(p.entries, entry)
	p.rebindIfOpen()
}

// modelRowLabel is the picker label of the sidebar's first row: the
// session's model. Enter or a double-click on it opens the model picker
// (the same dialog "/model" opens); the row is drawn from the top bar's
// session info, so this label is only the list's stable name for it.
const (
	modelRowLabel     = "model"
	contextRowLabel   = "context"
	filesHeaderLabel  = "files changed"
	agentsHeaderLabel = "subagents"
)

// rowLabels is the selectable list in render order, derived from the one
// row plan navGroups builds. Deriving it means the picker's list and the
// drawn rows cannot disagree about what is selectable or in what order -
// they used to be two hand-kept sequences.
func (p panel) rowLabels() []string {
	files, agents := p.visibleRows()
	sel := p.navSelectable()
	names := make([]string, 0, len(sel))
	for _, g := range sel {
		switch g.kind {
		case navContextHeader:
			names = append(names, contextRowLabel)
		case navModel:
			names = append(names, modelRowLabel)
		case navFilesHeader:
			names = append(names, filesHeaderLabel)
		case navFile:
			names = append(names, files[g.at].rowLabel())
		case navAgentsHeader:
			names = append(names, agentsHeaderLabel)
		case navAgent:
			names = append(names, agents[g.at].rowLabel())
		}
	}
	return names
}

// visibleRows returns the filter-narrowed files and subagents in the same
// order rowLabels() feeds the picker, so a cursor row index maps to them
// positionally. Concurrent subagents commonly share a name and status (four
// "reviewer" rows all "running"), which makes rowLabel() collide - matching
// by label text instead of position silently resolves every one of them to
// whichever duplicate appears first, no matter which row the cursor is on.
func (p panel) visibleRows() ([]fileEntry, []subagentRow) {
	return p.filterEntries(p.list.Filter())
}

// selectionKey names the selected row by what it IS (a file's path, a
// subagent's id, a section) rather than by its render label, which
// changes as statuses tick - so a rebind can hold the same row across a
// label change.
func (p panel) selectionKey() string {
	files, agents := p.visibleRows()
	g, ok := p.navCursor()
	if !ok {
		return ""
	}
	return navKeyOf(g, files, agents)
}

// modelRowSelected reports whether the list highlights the model row.
func (p panel) modelRowSelected() bool {
	g, ok := p.navCursor()
	return ok && g.kind == navModel
}

// sectionHeaderSelected reports whether the list highlights a foldable
// section header, which is what left/right and Enter act on.
func (p panel) sectionHeaderSelected() bool {
	g, ok := p.navCursor()
	return ok && g.collapsible()
}

// noteSelection records what the cursor is on, for the next rebind.
// Every deliberate cursor move calls it; live mutations must not.
func (p *panel) noteSelection() { p.selKey = p.selectionKey() }

func (p *panel) rebindIfOpen() {
	if !p.open {
		return
	}
	keep := p.selKey
	if keep == "" {
		keep = p.selectionKey()
	}
	p.list.Rebind(p.rowLabels())
	if keep == "" || p.list.Filter() != "" {
		// A filter may exclude the held row; Rebind has already clamped
		// the cursor to the filtered list, which is the best hold
		// available.
		p.noteSelection()
		return
	}
	// Re-find the held row through the same plan the list was built from
	// and the same naming selectionKey used to capture it, so a held
	// selection survives a fold, an unfold, and a row arriving above it.
	files, agents := p.visibleRows()
	for i, g := range p.navSelectable() {
		if navKeyOf(g, files, agents) == keep {
			p.list.MoveTo(i)
			p.selKey = keep
			return
		}
	}
	// The held row is gone (a file dropped, an agent cleared). Whatever
	// the clamp left under the cursor is the new selection.
	p.noteSelection()
}

// selectedAgent returns the subagent the list highlights, if any.
func (p panel) selectedAgent() (subagentRow, bool) {
	_, agents := p.visibleRows()
	g, ok := p.navCursor()
	if !ok || g.kind != navAgent || g.at < 0 || g.at >= len(agents) {
		return subagentRow{}, false
	}
	return agents[g.at], true
}

// the model row: opening the sidebar is how the model is changed.
func (p *panel) openPanel() {
	p.open, p.focused, p.dialog, p.dialogAgent = true, true, false, ""
	p.rebindIfOpen()
	p.list.Rebind(p.rowLabels())
	// The model row, not index 0: the context header now sits above it,
	// and opening the sidebar is still how the model gets changed.
	// navGroups always emits exactly one selectable navModel, so this
	// loop always finds it; there is no fallback arm to keep alive.
	for i, g := range p.navSelectable() {
		if g.kind == navModel {
			p.list.MoveTo(i)
			p.noteSelection()
			return
		}
	}
}

// selected returns the file entry the list highlights.
func (p panel) selected() (fileEntry, bool) {
	entries, _ := p.visibleRows()
	g, ok := p.navCursor()
	if !ok || g.kind != navFile || g.at < 0 || g.at >= len(entries) {
		return fileEntry{}, false
	}
	return entries[g.at], true
}

// contentRows is the selected file's content: its diff, or its
// post-edit source when sourceView is set.
func (p panel) contentRows(t theme.Theme, tier theme.Tier, width int) []string {
	if a, isAgent := p.selectedAgent(); isAgent {
		if len(a.Log) > 0 {
			return a.Log
		}
		return []string{"subagent: " + a.ID + " (" + a.Status + ")", "", "no detailed step log recorded"}
	}
	e, ok := p.selected()
	if !ok {
		if len(p.entries) == 0 && len(p.agents) == 0 {
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
	if lines := render.FormatDiffLines(t, tier, width, e.Diff); len(lines) > 0 {
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

// chatWidth is the width the chat column (transcript, approval, composer,
// status row) renders at: the reading pane's width while the panel
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
	h := s.height
	if h <= 0 {
		h = 24
	}
	th := h - s.reservedRows()
	if h >= 3 && !s.embedded {
		th = h - 2 - s.reservedRows()
	}
	if th < 0 {
		return 0
	}
	return th
}

// panelBodyRows is how many content rows the dialog can show at the
// current size, for the offset's half-page steps and clamp.
func (s Screen) panelBodyRows() int {
	return render.DialogBodyRows(s.transcriptHeight())
}

// scrollPanel moves the dialog's body window by half a page, clamped to
// the content: the tail must stay reachable and the offset must never
// go negative (a negative window reads as dead scroll keys).
func (s *Screen) scrollPanel(dir int) {
	s.panel.offset += dir * max(1, s.panelBodyRows()/2)
	dw, _ := s.dialogSize()
	bodyW := render.DialogBodyWidth(dw)
	rows := len(s.panel.contentRows(s.Theme, s.Tier, bodyW))
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
		return s.contentHeight() >= 8
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
func (s Screen) panelFilterEntries(needle string) ([]fileEntry, []subagentRow) {
	return s.panel.filterEntries(needle)
}

// selectNavRow maps a rendered row in the nav sidebar to the picker row
// drawn there, and moves the cursor to it. It reports false when the row
// belongs to no selectable group, so the caller can ignore the click.
//
// The map comes from the same navGroups plan and the same window bounds
// the renderer uses, so a click cannot land on a row other than the one
// under the pointer. contextRows is passed in rather than derived here
// because only the Screen knows how tall the context section draws.
func (p *panel) selectNavRow(clickRow, maxRows, contextRows int) bool {
	if clickRow < 0 {
		return false
	}
	plan := p.navGroups(contextRows)
	selGroup := navSelGroup(plan, p.list.CursorRow())
	startGroup, endGroup := panelWindowGroupBounds(navGroupLens(plan), selGroup, maxRows, false)

	line := 0
	for gIdx := startGroup; gIdx < endGroup; gIdx++ {
		g := plan[gIdx]
		if clickRow >= line && clickRow < line+g.lines {
			pickerIdx := navPickerIndex(plan, gIdx)
			if pickerIdx < 0 {
				return false
			}
			p.list.MoveTo(pickerIdx)
			p.noteSelection()
			return true
		}
		line += g.lines
	}
	return false
}

func (p panel) filterEntries(needle string) ([]fileEntry, []subagentRow) {
	visible := make([]fileEntry, 0, len(p.entries))
	for _, e := range p.entries {
		if needle == "" || strings.Contains(strings.ToLower(e.rowLabel()), needle) {
			visible = append(visible, e)
		}
	}
	agents := make([]subagentRow, 0, len(p.agents))
	for _, a := range p.agents {
		if needle == "" || strings.Contains(strings.ToLower(a.rowLabel()), needle) {
			agents = append(agents, a)
		}
	}
	return visible, agents
}

// openPanelDialogForSelected opens the diff dialog or subagent thread for
// the selected item. The model row has no content dialog: Enter and a
// double-click on it open the model picker instead (see handlePanelListKey
// and handleNavClick).
func (s *Screen) openPanelDialogForSelected() tea.Cmd {
	if s.panel.modelRowSelected() || !s.panelDialogFits() {
		return nil
	}
	var cmd tea.Cmd
	if a, isAgent := s.panel.selectedAgent(); isAgent {
		s.panel.dialogAgent = a.ID
		_, cmd = s.openThread(a.ID)
	} else {
		s.panel.dialogAgent = ""
	}
	s.panel.dialog, s.panel.offset = true, 0
	return cmd
}

// handleNavClick routes mouse clicks within the nav sidebar. Callers pass
// the screen row minus the top gutter (mouse.go's handleClick and
// handleModalClick): 0 is the pane's top padding row and the sidebar's own
// rows follow, the same row numbers the rendered View uses below the frame.
// The rows are not enumerated here because the context section's height
// varies with the body (contextSectionRows); selectNavRow derives the map
// from the same function the renderer uses, so the two cannot drift. A click
// on a file or subagent row opens its dialog; a click on the model row
// selects it, and a second click within the double-click window opens
// the model picker, as Enter does.
func (s *Screen) handleNavClick(clickRow int) (app.Screen, tea.Cmd) {
	// clickRow 0 is top padding row; content starts at clickRow 1
	clickRow--
	if clickRow < 0 {
		return *s, nil
	}
	s.panel.focused = true
	paneH := max(1, s.contentHeight())
	innerNavH := max(1, paneH-2)
	if !s.panel.selectNavRow(clickRow, innerNavH, s.contextSectionRows(innerNavH)) {
		return *s, nil
	}
	// A click on a section header folds or unfolds it: the header draws
	// the marker, so it is the affordance, and a marker the mouse cannot
	// work is a control that lies about being one.
	if s.panel.sectionHeaderSelected() {
		s.panel.toggleSection()
		return *s, nil
	}
	if s.panel.modelRowSelected() {
		now := time.Now()
		if s.now != nil {
			now = s.now()
		}
		double := !s.lastNavClickTime.IsZero() && now.Sub(s.lastNavClickTime) < 500*time.Millisecond && s.lastNavClickRow == clickRow
		s.lastNavClickTime, s.lastNavClickRow = now, clickRow
		if double {
			s.lastNavClickTime = time.Time{}
			return s.runSlashCommand("/model")
		}
		return *s, nil
	}
	cmd := s.openPanelDialogForSelected()
	return *s, cmd
}
