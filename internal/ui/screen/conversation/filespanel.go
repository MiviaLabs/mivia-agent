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
// subagent's id) rather than by its render label, which changes as
// statuses tick - so a rebind can hold the same row across a label
// change.
func (p panel) selectionKey() string {
	entries, agents := p.visibleRows()
	idx := p.list.CursorRow()
	if idx < 0 {
		return ""
	}
	if idx < len(entries) {
		return "f:" + entries[idx].Path
	}
	idx -= len(entries)
	if idx >= 0 && idx < len(agents) {
		return "a:" + agents[idx].ID
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
	entries, agents := p.visibleRows()
	idx := p.list.CursorRow() - len(entries)
	if idx < 0 || idx >= len(agents) {
		return subagentRow{}, false
	}
	return agents[idx], true
}

func matchesAgentID(aID, id string) bool {
	if aID == id {
		return true
	}
	if aID == "" || id == "" {
		return false
	}
	if idx := strings.Index(aID, ":"); idx >= 0 {
		if aID[idx+1:] == id {
			return true
		}
	}
	if idx := strings.Index(id, ":"); idx >= 0 {
		if id[idx+1:] == aID {
			return true
		}
	}
	return false
}

// observeAgentStart records or updates one subagent's running status. A
// start means a NEW task under an old id - not history. Leaving it
// terminal would badge a genuinely running dispatch as already finished.
// Start events carry no group/call identity that could distinguish a
// genuinely out-of-order start arriving after its own dispatch's end;
// resetting is the lesser evil - worst case a finished row briefly shows
// running until its end event re-terminates it.
func (p *panel) observeAgentStart(id, name string) {
	p.agents = slices.Clone(p.agents)
	for i, a := range p.agents {
		if matchesAgentID(a.ID, id) {
			if name != "" {
				p.agents[i].Name = name
			}
			if a.Status == "" || a.Status == "pending" || isTerminalStatus(a.Status) {
				p.agents[i].Status = "running"
				// A (re)created row is a NEW run under a reused id: anchor
				// its stall clock and its elapsed-time clock now, so the
				// fresh row never renders instantly "stalled", nor reports
				// the elapsed time of the run that already ended.
				now := time.Now()
				p.agents[i].LastProgress = now
				p.agents[i].StartedAt = now
			}
			p.rebindIfOpen()
			return
		}
	}
	now := time.Now()
	p.agents = append(p.agents, subagentRow{ID: id, Name: name, Status: "running", LastProgress: now, StartedAt: now})
	p.rebindIfOpen()
}

// observeAgentEnd updates a tracked subagent's terminal state upon completion or failure.
func (p *panel) observeAgentEnd(id string, ok bool) {
	status := "completed"
	if !ok {
		status = "failed"
	}
	p.agents = slices.Clone(p.agents)
	for i, a := range p.agents {
		if matchesAgentID(a.ID, id) {
			p.agents[i].Status = status
			p.agents[i].LastProgress = time.Now()
			p.rebindIfOpen()
			return
		}
	}
}

// observeAgentGroupStart registers a dispatch_tasks call's fanned-out
// per-task ids as one running row each - instead of observeAgentStart's
// single row for the whole call - and remembers the group under callID so
// observeAgentGroupEnd can resolve every member's terminal status when the
// outer call completes.
func (p *panel) observeAgentGroupStart(callID string, ids []string, names map[string]string) {
	if p.dispatchGroups == nil {
		p.dispatchGroups = map[string][]string{}
	}
	p.dispatchGroups[callID] = ids
	for _, id := range ids {
		name := ""
		if names != nil {
			name = names[id]
			if name == "" {
				prefix := callID + ":"
				rawID := strings.TrimPrefix(id, prefix)
				name = names[rawID]
			}
		}
		p.observeAgentStart(id, name)
	}
}

// observeAgentGroupEnd resolves a dispatch_tasks group's per-task terminal
// status from statuses (task id -> status, parsed from the tool's own JSON
// result), falling back to ok for any member statuses does not cover. A
// no-op when callID names no tracked group (the ordinary single-row path
// handles it instead).
func (p *panel) observeAgentGroupEnd(callID string, statuses map[string]string, ok bool) {
	ids, found := p.dispatchGroups[callID]
	if !found {
		return
	}
	delete(p.dispatchGroups, callID)
	prefix := callID + ":"
	for _, id := range ids {
		rawID := strings.TrimPrefix(id, prefix)
		status := statuses[id]
		if status == "" {
			status = statuses[rawID]
		}
		if status != "" {
			p.setAgentStatus(id, status)
			continue
		}
		p.observeAgentEnd(id, ok)
	}
}

// setAgentStatus overwrites one tracked subagent's status verbatim - unlike
// observeAgentEnd, which only ever writes "completed" or "failed".
func (p *panel) setAgentStatus(id, status string) {
	p.agents = slices.Clone(p.agents)
	for i, a := range p.agents {
		if matchesAgentID(a.ID, id) {
			p.agents[i].Status = status
			p.agents[i].LastProgress = time.Now()
			p.rebindIfOpen()
			return
		}
	}
}

// isDispatchGroup reports whether callID names a tracked dispatch_tasks
// group, so the caller can choose the group-aware end path over the
// ordinary single-row one.
func (p panel) isDispatchGroup(callID string) bool {
	_, found := p.dispatchGroups[callID]
	return found
}

// reconcileTerminal transitions all non-terminal subagents to a terminal state
// when a turn ends without explicit tool end events (cancellation, error, interrupt).
func (p *panel) reconcileTerminal(reason string) {
	status := statusCancelled
	switch reason {
	case "error", "failed":
		status = statusFailed
	case "interrupted":
		status = statusInterrupted
	case "completed":
		status = statusCompleted
	}
	p.agents = slices.Clone(p.agents)
	changed := false
	for i, a := range p.agents {
		if isNonTerminalStatus(a.Status) {
			p.agents[i].Status = status
			p.agents[i].LastProgress = time.Now()
			changed = true
		}
	}
	if changed {
		p.rebindIfOpen()
	}
}

// observeAgentHistory idempotently registers or updates a subagent from replayed history.
func (p *panel) observeAgentHistory(id, status string) {
	p.agents = slices.Clone(p.agents)
	for i, a := range p.agents {
		if matchesAgentID(a.ID, id) {
			if isNonTerminalStatus(a.Status) {
				p.agents[i].Status = status
			}
			p.rebindIfOpen()
			return
		}
	}
	p.agents = append(p.agents, subagentRow{ID: id, Status: status})
	p.rebindIfOpen()
}

// activeAgentCount returns the count of currently running/pending subagents.
func (p panel) activeAgentCount() int {
	count := 0
	for _, a := range p.agents {
		if isNonTerminalStatus(a.Status) {
			count++
		}
	}
	return count
}

// observeAgent records progress for a subagent. It preserves the subagent's
// starting state and name, updates steps/toolcalls, and advances the stall clock
// only when real forward progress occurs.
func (p *panel) observeAgent(id string, pr *uievent.Progress) {
	log := slices.Clone(pr.Log)
	p.agents = slices.Clone(p.agents)
	for i, a := range p.agents {
		if matchesAgentID(a.ID, id) {
			if isTerminalStatus(a.Status) && !isTerminalStatus(pr.Status) {
				return
			}
			row := a
			if pr.Status != "" {
				row.Status = pr.Status
			}
			if pr.Step > 0 {
				row.Step = pr.Step
			}
			if pr.TotalSteps > 0 {
				row.Total = pr.TotalSteps
			}
			if pr.ToolCalls > 0 {
				row.ToolCalls = pr.ToolCalls
			}
			if len(log) > 0 {
				combinedLog := make([]string, 0, len(a.Log)+len(log))
				combinedLog = append(combinedLog, a.Log...)
				combinedLog = append(combinedLog, log...)
				row.Log = combinedLog
			}
			if progressAdvances(a, row) {
				row.LastProgress = time.Now()
			}
			p.agents[i] = row
			p.rebindIfOpen()
			return
		}
	}
	now := time.Now()
	row := subagentRow{
		ID:           id,
		Status:       pr.Status,
		Step:         pr.Step,
		Total:        pr.TotalSteps,
		ToolCalls:    pr.ToolCalls,
		Log:          log,
		LastProgress: now,
		StartedAt:    now,
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
	entries, _ := p.visibleRows()
	idx := p.list.CursorRow()
	if idx < 0 || idx >= len(entries) {
		return fileEntry{}, false
	}
	return entries[idx], true
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

// selectNavRow maps a rendered row in the nav sidebar to an entry index in the picker.
func (p *panel) selectNavRow(clickRow int) bool {
	// row 0 is SIDEBAR title
	// row 1 is "files changed (N)" header
	if clickRow <= 1 {
		return false
	}
	fileIdx := clickRow - 2
	if fileIdx >= 0 && fileIdx < len(p.entries) {
		p.list.MoveTo(fileIdx)
		return true
	}
	subHeader := 2 + len(p.entries)
	if clickRow == subHeader {
		return false
	}
	agentIdx := clickRow - (subHeader + 1)
	if agentIdx >= 0 && agentIdx < len(p.agents) {
		p.list.MoveTo(len(p.entries) + agentIdx)
		return true
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

// openPanelDialogForSelected opens the diff dialog or subagent thread for the selected item.
func (s *Screen) openPanelDialogForSelected() tea.Cmd {
	if !s.panelDialogFits() {
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

// handleNavClick routes mouse clicks within the nav sidebar.
func (s *Screen) handleNavClick(clickRow int) (app.Screen, tea.Cmd) {
	// clickRow 0 is top padding row; content starts at clickRow 1
	clickRow--
	if clickRow < 0 {
		return *s, nil
	}
	s.panel.focused = true
	if s.panel.selectNavRow(clickRow) {
		cmd := s.openPanelDialogForSelected()
		return *s, cmd
	}
	return *s, nil
}
