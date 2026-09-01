package conversation

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// panelWindowGroups clips row-groups to a window around the selected group,
// reserving one line for the filter indicator when one will be appended,
// and NEVER splitting a group across the clip boundary: an agent's name
// line and its metrics line must stay together, or the metrics line would
// render stranded with no name line above it. A window that cannot land on
// exact line-count parity because a group would have to be split grows
// outward to the next whole group instead of splitting one.
func panelWindowGroups(groups [][]string, selGroup, maxRows int, filterActive bool) [][]string {
	startGroup, endGroup := panelWindowRange(groups, selGroup, maxRows, filterActive)
	return groups[startGroup:endGroup]
}

// panelWindowRange computes the start and end group indices for windowing groups.
func panelWindowRange(groups [][]string, selGroup, maxRows int, filterActive bool) (int, int) {
	groupLens := make([]int, len(groups))
	for i, g := range groups {
		groupLens[i] = len(g)
	}
	return panelWindowGroupBounds(groupLens, selGroup, maxRows, filterActive)
}

// panelWindowGroupBounds computes the [startGroup, endGroup) slice bounds for row-groups
// given their individual line heights.
func panelWindowGroupBounds(groupLens []int, selGroup, maxRows int, filterActive bool) (int, int) {
	limit := maxRows
	if filterActive && limit > 1 {
		limit--
	}
	if limit <= 0 || len(groupLens) == 0 {
		return 0, len(groupLens)
	}
	offsets := make([]int, len(groupLens)+1)
	total := 0
	for i, l := range groupLens {
		offsets[i] = total
		total += l
	}
	offsets[len(groupLens)] = total
	if total <= limit {
		return 0, len(groupLens)
	}
	selRow := 0
	if selGroup >= 0 && selGroup < len(groupLens) {
		selRow = offsets[selGroup]
	}
	start := 0
	if selRow >= limit {
		start = selRow - limit + 1
	}
	if start > total-limit {
		start = total - limit
	}
	end := start + limit
	startGroup := 0
	for startGroup < len(groupLens) && offsets[startGroup+1] <= start {
		startGroup++
	}
	endGroup := len(groupLens)
	for endGroup > 0 && offsets[endGroup-1] >= end {
		endGroup--
	}
	return startGroup, endGroup
}

// panelGroupLens returns the line count of each group in the sidebar layout:
// 1 line for SIDEBAR title, 1 line for files header, 1 line per file entry,
// 1 line for subagents header, and 2 lines per agent row.
func panelGroupLens(fileCount, agentCount int) []int {
	lens := make([]int, 0, 3+fileCount+agentCount)
	lens = append(lens, 1) // SIDEBAR title
	lens = append(lens, 1) // files changed header
	for i := 0; i < fileCount; i++ {
		lens = append(lens, 1)
	}
	lens = append(lens, 1) // subagents header
	for i := 0; i < agentCount; i++ {
		lens = append(lens, 2)
	}
	return lens
}

// panelGroupToPickerIdx maps a group index to its corresponding picker cursor index,
// or -1 if the group is a non-selectable header (SIDEBAR, files header, subagents header).
func panelGroupToPickerIdx(gIdx, fileCount, agentCount int) int {
	if gIdx < 2 {
		return -1
	}
	if gIdx < 2+fileCount {
		return gIdx - 2
	}
	if gIdx == 2+fileCount {
		return -1
	}
	agentIdx := gIdx - (3 + fileCount)
	if agentIdx >= 0 && agentIdx < agentCount {
		return fileCount + agentIdx
	}
	return -1
}

// panelSelGroup computes the group index for the currently selected picker item.
func panelSelGroup(selIdx, fileCount, agentCount int) int {
	if selIdx < 0 {
		return -1
	}
	if selIdx < fileCount {
		return 2 + selIdx
	}
	agentIdx := selIdx - fileCount
	if agentIdx < agentCount {
		return 3 + fileCount + agentIdx
	}
	return -1
}

// flattenGroups concatenates row-groups back into a flat line list, in
// order, for final width-clipping and rendering.
func flattenGroups(groups [][]string) []string {
	var rows []string
	for _, g := range groups {
		rows = append(rows, g...)
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

func (s Screen) panelFileRow(e fileEntry, selected bool) string {
	name, dir := splitPath(e.Path)
	var glyph string
	switch e.Kind {
	case fileCreated:
		glyph = "+"
	case fileDeleted:
		glyph = "-"
	default:
		glyph = "~"
	}
	prefix, style := "  ", render.Role(s.Theme, s.Tier, theme.RoleFG)
	if selected {
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

// statusBadgeRole maps a subagent row's display status to its badge color.
// theme.RoleInfo is the default for "running"/"pending" (no explicit case),
// so every terminal status needs its own case here - otherwise it renders
// indistinguishably from an actively running row, defeating the point of
// adding it to the terminal vocabulary (isTerminalStatus).
func statusBadgeRole(status string) theme.Role {
	switch status {
	case "completed", "done":
		return theme.RoleSuccess
	case "failed", "error", "interrupted", "timed_out":
		return theme.RoleDanger
	case "cancelled", "canceled":
		return theme.RoleFGSubtle
	case "thinking":
		return theme.RoleWarning
	case statusStalled:
		return theme.RoleWarning
	default:
		return theme.RoleInfo
	}
}

// formatElapsed renders a duration as the sidebar's compact elapsed label
// ("10m 40s", "45s", "1h 05m"). internal/ui/** may not import
// internal/clichat (UI isolation, docs/design/ui-isolation.md), so this
// does not reuse clichat.FormatDuration's tighter "10m40s" shape.
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int(d/time.Minute) % 60
	s := int(d/time.Second) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// elapsedFor computes a row's displayed elapsed time as of now. A running
// row's elapsed grows with the clock; a terminal row's freezes at
// LastProgress (stamped the moment observeAgentEnd/setAgentStatus settled
// it), so a completed/failed/timed-out row does not keep counting up on
// every render after the subagent already finished.
func elapsedFor(a subagentRow, now time.Time) time.Duration {
	if a.StartedAt.IsZero() {
		return 0
	}
	if isTerminalStatus(a.Status) {
		return a.LastProgress.Sub(a.StartedAt)
	}
	return now.Sub(a.StartedAt)
}

// panelAgentRow renders one subagent as two lines: the name/status badge
// (selectable, matches rowLabel), and an indented metrics line carrying
// Elapsed/Tools/Step - moved here from the chat transcript (which used to
// live-rewrite a churning "elapsed=Xs steps=N" line into the middle of the
// scrollback on every heartbeat) so this sidebar row is the one live-updating
// surface for subagent progress.
func (s Screen) panelAgentRow(a subagentRow, selected bool) []string {
	prefix := "  · "
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	fg := render.Role(s.Theme, s.Tier, theme.RoleFG)
	if selected {
		prefix = "> · "
		fg = render.WithBg(fg, s.Theme, s.Tier, theme.RoleBGSelection)
	}
	border := render.Role(s.Theme, s.Tier, theme.RoleBorder)
	// displayStatus derives "stalled" at render time from the row's stall
	// clock (agent_stall.go); the stored status itself never changes.
	status := s.panel.displayStatus(a)
	var statusBadge string
	if status != "" {
		statusStyle := render.Role(s.Theme, s.Tier, statusBadgeRole(status))
		statusBadge = " " + border.Render("[") + statusStyle.Render(status) + border.Render("]")
	}
	nameLine := subtle.Render(prefix) + fg.Render(a.displayName()) + statusBadge
	metrics := fmt.Sprintf("Elapsed: %s, Tools: %d, Step: %d",
		formatElapsed(elapsedFor(a, s.now())), a.ToolCalls, a.Step)
	metricsLine := subtle.Render("      " + metrics)
	return []string{nameLine, metricsLine}
}

func (s Screen) panelRows(inner, maxRows int) []string {
	visible, agents := s.panelFilterEntries("")

	// selIdx is the picker's cursor row: the list is built files-then-
	// agents in this exact order (rowLabels), so position - not the
	// rendered label - is what identifies the highlighted row. Comparing
	// by label instead marks every row that happens to render identically
	// (concurrent same-named agents sharing a status, e.g. four
	// "reviewer" rows all "running"), painting ">" on all of them.
	selIdx := s.panel.list.CursorRow()
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	marked := s.panel.focused

	var groups [][]string
	selGroup := -1

	if marked {
		groups = append(groups, []string{render.Role(s.Theme, s.Tier, theme.RoleAccent).Bold(true).Render("● SIDEBAR") + " " + subtle.Render("(focused)")})
	} else {
		groups = append(groups, []string{subtle.Render("  SIDEBAR")})
	}

	groups = append(groups, []string{subtle.Render("files changed (" + strconv.Itoa(len(visible)) + ")")})
	for i, e := range visible {
		if i == selIdx {
			selGroup = len(groups)
		}
		groups = append(groups, []string{s.panelFileRow(e, marked && i == selIdx)})
	}
	groups = append(groups, []string{subtle.Render("subagents (" + strconv.Itoa(len(agents)) + ")")})
	for i, a := range agents {
		idx := len(visible) + i
		if idx == selIdx {
			selGroup = len(groups)
		}
		groups = append(groups, s.panelAgentRow(a, marked && idx == selIdx))
	}

	groups = panelWindowGroups(groups, selGroup, maxRows, false)
	return clipRowsToWidth(flattenGroups(groups), inner)
}

// dialogParts is the content dialog's title, body, and hint. A
// subagent entry with a live thread renders the embedded conversation
// Screen, sized to the exact body area Dialog gives it; one without
// falls back to the step log the progress events carried. File entries
// show their windowed diff or source as before.
func (s Screen) dialogParts() (title, body, hint string) {
	dw, _ := s.dialogSize()
	bodyW := render.DialogBodyWidth(dw)
	if s.panel.dialogAgent != "" {
		for _, a := range s.panel.agents {
			if a.ID != s.panel.dialogAgent {
				continue
			}
			if s.thread != nil && s.threadID == s.panel.dialogAgent {
				s.thread.setSurface(bodyW, s.panelBodyRows())
				title = "subagent: " + a.displayName()
				return title, s.thread.View(), "esc close"
			}
			rows := append([]string{a.displayName() + "  " + a.Status}, a.Log...)
			return "subagent: " + a.displayName(), strings.Join(rows, "\n"), "any key closes"
		}
		if s.thread != nil && s.threadID == s.panel.dialogAgent {
			s.thread.setSurface(bodyW, s.panelBodyRows())
			title = "subagent: " + s.panel.dialogAgent
			return title, s.thread.View(), "esc close"
		}
		return "subagent: " + s.panel.dialogAgent, "", "any key closes"
	}
	title = "files"
	if e, ok := s.panel.selected(); ok {
		title = e.Path
	}
	rows := s.panel.contentRows(s.Theme, s.Tier, bodyW)
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
	paneH := max(1, s.contentHeight())
	readingW, navW := render.SplitWidths(w)

	innerNavW := max(1, navW-3)
	innerNavH := max(1, paneH-2)

	s.topbar.SetWidth(readingW)

	focus := render.Left
	if s.panel.focused {
		focus = render.Right
	}
	var chat []string
	chat = append(chat, strings.Split(s.topbar.View(), "\n")...)
	chat = append(chat, "")
	chat = append(chat, s.centerRows()...)
	chat = append(chat, s.chatTailRows()...)
	if len(chat) > paneH {
		chat = chat[:paneH]
	}
	frame := render.Split(s.Theme, s.Tier, w, paneH, focus,
		strings.Join(chat, "\n"), strings.Join(s.panelRows(innerNavW, innerNavH), "\n"))
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
// composer, status row) keeps its place.
func (s Screen) narrowPanelRows() []string {
	h := s.transcriptHeight()
	w := contentWidth(s.width)
	if s.panel.dialog && s.panelDialogFits() {
		title, body, hint := s.dialogParts()
		dw, dh := s.dialogSize()
		return overlayRows(render.Dialog(s.Theme, s.Tier, dw, dh, title, body, hint), h)
	}
	return overlayRows(strings.Join(s.panelRows(w, h), "\n"), h)
}

// chatTailRows is the chat column below the transcript area: the
// approval prompt when armed, the composer, and the status row.
func (s Screen) chatTailRows() []string {
	var rows []string
	if v := s.approval.View(); v != "" {
		rows = append(rows, strings.Split(v, "\n")...)
	}
	if v := s.history.View(); v != "" {
		rows = append(rows, strings.Split(v, "\n")...)
	}
	if v := s.queueOverlay.View(); v != "" {
		rows = append(rows, strings.Split(v, "\n")...)
	}
	if v := s.blackboard.View(); v != "" {
		rows = append(rows, strings.Split(v, "\n")...)
	}
	if !s.hideComposer {
		rows = append(rows, strings.Split(s.composer.View(), "\n")...)
	}
	rows = append(rows, s.statusRow())
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
	case s.palettePicker != nil:
		dw, dh := s.dialogSize()
		return overlayRows(renderPickerDialog(s.Theme, s.Tier, dw, dh, "command palette", *s.palettePicker), s.transcriptHeight())
	case s.effortPicker != nil:
		dw, dh := s.dialogSize()
		return overlayRows(renderPickerDialog(s.Theme, s.Tier, dw, dh, "select reasoning effort", *s.effortPicker), s.transcriptHeight())
	case s.login != nil:
		dw, dh := s.dialogSize()
		return overlayRows(renderLoginDialog(s.Theme, s.Tier, dw, dh, *s.login), s.transcriptHeight())
	case s.panel.dialog && s.panelDialogFits():
		title, body, hint := s.dialogParts()
		dw, dh := s.dialogSize()
		return overlayRows(render.Dialog(s.Theme, s.Tier, dw, dh, title, body, hint), s.transcriptHeight())
	case s.overlay != "":
		return overlayRows(s.overlay, s.transcriptHeight())
	case !s.embedded && s.transcript.Empty():
		return s.welcome.Rows(s.chatWidth(), s.transcriptHeight())
	default:
		return s.transcript.Rows()
	}
}
