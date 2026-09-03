package conversation

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
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

// panelWindowGroupBounds computes the [startGroup, endGroup) slice bounds
// for row-groups given their individual line heights.
//
// The window is built out of WHOLE groups and never exceeds limit lines.
// Both halves of that matter, and the previous version had only the
// first: it picked a line range and then widened it outwards to whole
// groups at each end, so a two-line agent group straddling either
// boundary pushed the window over the limit. The caller draws into a
// fixed pane and clips the overflow off the BOTTOM, so the rows lost
// were the selected agent's - the row the window exists to show. With
// twenty agents the selection fell off the pane entirely.
//
// The anchor group is always whole and always inside: it is the row the
// next key acts on, and a window that clips it is a window that hides
// the selection. Growth goes upward first, so a selection deep in a long
// list settles at the BOTTOM of the pane - where the newest subagent is,
// and where the eye already is on a list that has been growing.
//
// A single group taller than the whole limit still overflows; there is
// no window that both holds it whole and fits. It is returned alone, so
// what survives the caller's clip is its first line rather than someone
// else's.
func panelWindowGroupBounds(groupLens []int, selGroup, maxRows int, filterActive bool) (int, int) {
	limit := maxRows
	if filterActive && limit > 1 {
		limit--
	}
	if limit <= 0 || len(groupLens) == 0 {
		return 0, len(groupLens)
	}
	total := 0
	for _, l := range groupLens {
		total += l
	}
	if total <= limit {
		return 0, len(groupLens)
	}
	anchor := selGroup
	if anchor < 0 || anchor >= len(groupLens) {
		anchor = 0
	}
	lo, hi := anchor, anchor+1
	used := groupLens[anchor]
	for lo > 0 && used+groupLens[lo-1] <= limit {
		lo--
		used += groupLens[lo]
	}
	for hi < len(groupLens) && used+groupLens[hi] <= limit {
		used += groupLens[hi]
		hi++
	}
	return lo, hi
}

// panelGroupLens returns the line count of each group in the sidebar layout:
// contextRows single-line groups for the context section, then 1 for the model
// header, 1 for the model row, 1 for the files header, 1 per file entry, 1 for
// the subagents header, and 2 per agent row. The context section's height is a
// parameter because it grows a detail block on a tall terminal
// (contextSectionRows); hard-coding it here would put the click map and the
// drawn rows one section out of step.
func panelGroupLens(contextRows, fileCount, agentCount int) []int {
	lens := make([]int, 0, contextRows+4+fileCount+agentCount)
	for i := 0; i < contextRows; i++ {
		lens = append(lens, 1) // one context row
	}
	lens = append(lens, 1) // model header
	lens = append(lens, 1) // model row
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

// panelGroupToPickerIdx maps a group index to its corresponding picker cursor
// index, or -1 if the group is not selectable (every context row, the model
// header, the files header, the subagents header). Picker index 0 is the model
// row, the group right after the context section and its header.
func panelGroupToPickerIdx(gIdx, contextRows, fileCount, agentCount int) int {
	switch {
	case gIdx == contextRows+1:
		return 0
	case gIdx < contextRows+3:
		return -1
	case gIdx < contextRows+3+fileCount:
		return 1 + (gIdx - (contextRows + 3))
	case gIdx == contextRows+3+fileCount:
		return -1
	}
	agentIdx := gIdx - (contextRows + 4 + fileCount)
	if agentIdx >= 0 && agentIdx < agentCount {
		return 1 + fileCount + agentIdx
	}
	return -1
}

// panelSelGroup computes the group index for the currently selected picker item.
func panelSelGroup(selIdx, contextRows, fileCount, agentCount int) int {
	if selIdx < 0 {
		return -1
	}
	if selIdx == 0 {
		return contextRows + 1 // the model row
	}
	selIdx-- // past the model row
	if selIdx < fileCount {
		return contextRows + 3 + selIdx
	}
	agentIdx := selIdx - fileCount
	if agentIdx < agentCount {
		return contextRows + 4 + fileCount + agentIdx
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

// panelModelRow draws the sidebar's model row: the provider dimmed and
// the model name in the foreground role, with the same "> " marker and
// selection background the file rows use. The context share is its own
// section (panelContextRows), not a suffix here.
func (s Screen) panelModelRow(selected bool) string {
	info := s.topbar.Info()
	name := info.Name
	if name == "" {
		name = "no model"
	}
	prefix, style := "  ", render.Role(s.Theme, s.Tier, theme.RoleFG)
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	if selected {
		prefix = "> "
		style = render.WithBg(style, s.Theme, s.Tier, theme.RoleBGSelection)
		subtle = render.WithBg(subtle, s.Theme, s.Tier, theme.RoleBGSelection)
	}
	row := subtle.Render(prefix)
	if info.Provider != "" {
		row += subtle.Render(info.Provider + "/")
	}
	row += style.Render(name)
	return row
}

// contextDetailMinRows is the sidebar body height at which the context
// section earns its detail block. Below it the section stays at its three
// summary rows so files and subagents - the live things - keep the column.
const contextDetailMinRows = 24

// contextSummaryRows and contextDetailRows are the section's two heights:
// header + bar + totals, and those plus one row per bucket. A capped budget
// adds one more row in either mode to say so.
const (
	contextSummaryRows = 3
	contextDetailRows  = contextSummaryRows + 8
)

// contextSectionRows is how many rows the context section draws in a sidebar
// body of maxRows. Every consumer of the sidebar's group map calls it, so the
// click map and the drawn rows cannot disagree about the section's height.
//
// The height depends on the body and on whether the budget is capped, both of
// which are fixed for a session, so nothing below the section moves as tokens
// accumulate.
func (s Screen) contextSectionRows(maxRows int) int {
	rows := contextSummaryRows
	if maxRows >= contextDetailMinRows {
		rows = contextDetailRows
	}
	if s.topbar.Info().BudgetIsCapped() {
		rows++
	}
	return rows
}

// tokensShort renders a token count the way the sidebar's narrow column can
// carry it: "940", "21k", "1.2M". It matches chat.FormatTokenK's k convention
// rather than inventing a second one, and adds the M step because a 1M-window
// model would otherwise print a four-digit k.
func tokensShort(n int64) string {
	switch {
	case n < 0:
		return "0"
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return strconv.FormatInt(n/1000, 10) + "k"
	default:
		// One decimal, but never a bare ".0": a 1M window is "1M", not
		// "1.0M", and the extra glyph is a column the sidebar cannot spare.
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64), ".0") + "M"
	}
}

// panelSpreadRow lays a label against a value across the sidebar's full inner
// width, the label left and the value right. When the two cannot both fit the
// value wins: it is the number the row exists to report, and a clipped label
// is still readable from its first letters.
func panelSpreadRow(inner int, label, value string, labelStyle, valueStyle lipgloss.Style) string {
	if inner <= 0 {
		return ""
	}
	valueW := ansi.StringWidth(value)
	if valueW >= inner {
		return valueStyle.Render(ansi.Truncate(value, inner, ""))
	}
	label = ansi.Truncate(label, max(0, inner-valueW-1), "")
	gap := inner - ansi.StringWidth(label) - valueW
	return labelStyle.Render(label) + strings.Repeat(" ", gap) + valueStyle.Render(value)
}

// panelContextBar draws the fill as two runs rather than one: the floor - the
// system prompt, tool schemas and carried memory that are on every request
// whatever was said - in the dimmest role, and the conversation on top of it
// in the share's own role. The split is the actionable one, because only the
// second run is what compaction can give back. Empty cells keep the hollow
// glyph, so the floor run stays distinguishable from open space even in a
// theme where the two roles are close.
func (s Screen) panelContextBar(inner, pct, floorPct int) string {
	if inner <= 0 {
		return ""
	}
	full, empty := render.ContextGlyphs(s.Tier)
	fill := render.ContextCells(pct, inner)
	floor := min(fill, render.ContextCells(floorPct, inner))
	border := render.Role(s.Theme, s.Tier, theme.RoleBorder)
	share := render.Role(s.Theme, s.Tier, render.ContextRole(pct))
	return border.Render(strings.Repeat(full, floor)) +
		share.Render(strings.Repeat(full, fill-floor)) +
		border.Render(strings.Repeat(empty, inner-fill))
}

// panelContextRows draws the sidebar's first section. Three rows always: a
// "context" header with the share of the budget in use, the two-tone bar, and
// what that share is in tokens. On a tall enough body (contextDetailMinRows) a
// bucket block follows, one row each, answering the question the bar raises -
// which of these can I actually get back. The rows sum to the header's own
// number because the accounting scales them to it (chat.ContextBreakdown).
//
// The row count depends only on maxRows, never on what the session holds, so
// nothing below the section moves as tokens accumulate (ux-rules 2.7).
func (s Screen) panelContextRows(inner, maxRows int) []string {
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	border := render.Role(s.Theme, s.Tier, theme.RoleBorder)
	pct, known := s.topbar.ContextPercent()
	usage := s.topbar.Usage()
	budget := s.topbar.Info().ContextWindow

	share, shareStyle := "unknown", subtle
	if known {
		share, shareStyle = strconv.Itoa(pct)+"%", render.Role(s.Theme, s.Tier, render.ContextRole(pct))
	}
	rows := []string{
		panelSpreadRow(inner, "context", share, subtle, shareStyle),
		s.panelContextBar(inner, pct, floorPercent(usage, budget)),
	}

	totals := ""
	if known {
		totals = panelSpreadRow(inner,
			tokensShort(usage.InputTokens)+" of "+tokensShort(budget),
			tokensShort(max(0, budget-usage.InputTokens))+" free", border, border)
	}
	rows = append(rows, totals)

	// A budget far below the model's own window is a choice made in config,
	// not the model's limit. Unsaid, the gauge reads as capacity that went
	// missing: a 400k budget on a 1M-window model looks like 600k lost.
	if info := s.topbar.Info(); info.BudgetIsCapped() {
		rows = append(rows, panelSpreadRow(inner,
			"capped from "+tokensShort(info.DeclaredWindow), "", border, border))
	}

	if maxRows < contextDetailMinRows {
		return rows
	}
	b := usage.Breakdown
	for _, bucket := range []struct {
		label  string
		tokens int64
	}{
		{"system", b.System},
		{"tools (" + strconv.Itoa(b.ToolCount) + ")", b.ToolSchemas},
		// Server-supplied schemas get their own row because they are the part
		// of the floor an operator can actually remove, by turning a server
		// off. Drawn at zero when no server is connected, so the block keeps
		// its height.
		{"servers (" + strconv.Itoa(b.ExternalToolCount) + ")", b.ExternalSchemas},
		{"memory", b.Memory + b.Summary},
		{"messages", b.Prose},
		{"results", b.ToolResults},
		{"thinking", b.Reasoning},
		// What the provider is pricing that the session has not adopted yet.
		// It empties into the rows above when the turn finishes.
		{"this turn", b.Pending},
	} {
		rows = append(rows, panelSpreadRow(inner, bucket.label, tokensShort(bucket.tokens), border, subtle))
	}
	return rows
}

// floorPercent is the share of the budget taken by the parts compaction
// cannot reclaim. It returns 0 when the budget is unknown, so an unbound
// session draws no floor rather than a bar computed against nothing.
func floorPercent(usage ports.Usage, budget int64) int {
	if budget <= 0 {
		return 0
	}
	return int(usage.Breakdown.Floor() * 100 / budget)
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
	metricsLine := subtle.Render(agentMetrics(a, elapsedFor(a, s.now()), s.panelInnerWidth()))
	return []string{nameLine, metricsLine}
}

// agentMetricsIndent aligns the metrics line under the name text, past
// the "  · " prefix the name line draws.
const agentMetricsIndent = "      "

// agentMetrics is a subagent row's second line, fitted to the sidebar.
//
// It DEGRADES rather than clips. The line was one fixed string cut to
// width by clipRowsToWidth, which on a narrow sidebar produced
// "Elapsed: 0s, Tools:" - a label with its number sliced off, the one
// part of the line that carries information. Dropping whole facts from
// the right instead means every fact still on the line is complete, and
// the elapsed time - the fact a reader watching a long run actually
// wants - is the last one to go.
//
// The " · " join is the compact meta grammar the transcript's own header
// meta uses, so the two surfaces read alike.
func agentMetrics(a subagentRow, elapsed time.Duration, inner int) string {
	parts := []string{
		formatElapsed(elapsed),
		strconv.Itoa(a.ToolCalls) + " tools",
		"step " + strconv.Itoa(a.Step),
	}
	budget := inner - len(agentMetricsIndent)
	for n := len(parts); n > 1; n-- {
		line := strings.Join(parts[:n], " · ")
		if ansi.StringWidth(line) <= budget {
			return agentMetricsIndent + line
		}
	}
	return agentMetricsIndent + parts[0]
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

	// The context and model sections replace the old SIDEBAR title: the
	// top bar hides its capsule and badge while the panel is open, so
	// each is named once. Focus is signalled by the "> " marker on the
	// selected row, not by a header. The context section is two fixed
	// rows (header, bar) and never selectable.
	for _, row := range s.panelContextRows(inner, maxRows) {
		groups = append(groups, []string{row})
	}
	groups = append(groups, []string{subtle.Render("model")})
	if selIdx == 0 {
		selGroup = len(groups)
	}
	groups = append(groups, []string{s.panelModelRow(marked && selIdx == 0)})

	groups = append(groups, []string{subtle.Render("files changed (" + strconv.Itoa(len(visible)) + ")")})
	for i, e := range visible {
		idx := 1 + i
		if idx == selIdx {
			selGroup = len(groups)
		}
		groups = append(groups, []string{s.panelFileRow(e, marked && idx == selIdx)})
	}
	groups = append(groups, []string{subtle.Render("subagents (" + strconv.Itoa(len(agents)) + ")")})
	for i, a := range agents {
		idx := 1 + len(visible) + i
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

// panelInnerWidth is the sidebar's usable column count in the wide
// layout: the nav pane minus its gutter. The context bar fills it.
func (s Screen) panelInnerWidth() int {
	_, navW := render.SplitWidths(contentWidth(s.width))
	return max(1, navW-3)
}

// panelFrameRows draws the wide layout's panes and returns exactly
// paneH rows: the chat column in the left reading pane (or the content
// dialog over that pane, with the list still visible beside it) and the
// file list in the right nav pane.
func (s Screen) panelFrameRows() []string {
	w := contentWidth(s.width)
	paneH := max(1, s.contentHeight())
	readingW, _ := render.SplitWidths(w)

	innerNavW := s.panelInnerWidth()
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
