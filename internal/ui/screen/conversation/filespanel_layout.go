package conversation

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

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
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	fg := render.Role(s.Theme, s.Tier, theme.RoleFG)
	if marked && a.rowLabel() == selLabel {
		prefix = "> · "
		fg = render.WithBg(fg, s.Theme, s.Tier, theme.RoleBGSelection)
	}
	border := render.Role(s.Theme, s.Tier, theme.RoleBorder)
	var statusBadge string
	if a.Status != "" {
		role := theme.RoleInfo
		switch a.Status {
		case "completed", "done":
			role = theme.RoleSuccess
		case "failed", "error", "interrupted":
			role = theme.RoleDanger
		case "cancelled", "canceled":
			role = theme.RoleFGSubtle
		case "thinking":
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
	visible, agents := s.panelFilterEntries("")

	selLabel, _ := s.panel.list.Selected()
	subtle := render.Role(s.Theme, s.Tier, theme.RoleFGSubtle)
	marked := s.panel.focused

	var rows []string
	selRow := -1

	if marked {
		rows = append(rows, render.Role(s.Theme, s.Tier, theme.RoleAccent).Bold(true).Render("● SIDEBAR")+" "+subtle.Render("(focused)"))
	} else {
		rows = append(rows, subtle.Render("  SIDEBAR"))
	}

	rows = append(rows, subtle.Render("files changed ("+strconv.Itoa(len(visible))+")"))
	for _, e := range visible {
		if e.rowLabel() == selLabel {
			selRow = len(rows)
		}
		rows = append(rows, s.panelFileRow(e, selLabel, marked))
	}
	rows = append(rows, subtle.Render("subagents ("+strconv.Itoa(len(agents))+")"))
	for _, a := range agents {
		if a.rowLabel() == selLabel {
			selRow = len(rows)
		}
		rows = append(rows, s.panelAgentRow(a, selLabel, marked))
	}

	rows = panelWindowRows(rows, selRow, maxRows, false)
	return clipRowsToWidth(rows, inner)
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
		if s.thread != nil && s.threadID == s.panel.dialogAgent {
			s.thread.setSurface(bodyW, s.panelBodyRows())
			title = "subagent: " + s.panel.dialogAgent
			return title, s.thread.View(), "esc close"
		}
		for _, a := range s.panel.agents {
			if a.ID != s.panel.dialogAgent {
				continue
			}
			rows := append([]string{a.ID + "  " + a.Status}, a.Log...)
			return "subagent: " + a.ID, strings.Join(rows, "\n"), "any key closes"
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
