package cli

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type agentDialog struct {
	rows   []agentListRow
	cursor int
	scroll int
	notice string
	busy   bool
}

func newAgentDialog(rows []agentListRow, busy bool) *agentDialog {
	d := &agentDialog{rows: rows, busy: busy}
	for i, row := range rows {
		if row.Current {
			d.cursor = i
			break
		}
	}
	return d
}

func agentDialogPrefs() dialogPrefs {
	return dialogPrefs{preferredWPct: 70, preferredHPct: 60, minW: 28, minH: 8, frameCols: 4, frameRows: 3, pager: true}
}

func (d *agentDialog) move(delta int) {
	if len(d.rows) == 0 || delta == 0 {
		return
	}
	d.cursor += delta
	if d.cursor < 0 {
		d.cursor = 0
	}
	if d.cursor >= len(d.rows) {
		d.cursor = len(d.rows) - 1
	}
	d.clampScroll(1)
}

func (d *agentDialog) clampScroll(page int) {
	page = max(1, page)
	if d.cursor < d.scroll {
		d.scroll = d.cursor
	}
	if d.cursor >= d.scroll+page {
		d.scroll = d.cursor - page + 1
	}
	d.scroll = max(0, min(d.scroll, max(0, len(d.rows)-page)))
}

func (d *agentDialog) layout(w, h int) dialogLayout {
	return makeDialogLayout(w, h, agentDialogPrefs(), func(innerW int) (int, int) {
		rows := d.rowLinesAt(innerW, len(d.rows), 0)
		maxW := 0
		for _, row := range rows {
			maxW = max(maxW, ansi.StringWidth(row))
		}
		return maxW, len(rows)
	})
}

func (d *agentDialog) rowLines(inner, visible int) []string {
	visible = max(1, visible)
	d.clampScroll(visible)
	return d.rowLinesAt(inner, visible, d.scroll)
}

func (d *agentDialog) rowLinesAt(inner, visible, scroll int) []string {
	visible = max(1, visible)
	if len(d.rows) == 0 {
		return []string{tuiDimStyle.Render("no agents loaded")}
	}
	scroll = max(0, min(scroll, max(0, len(d.rows)-visible)))
	end := min(len(d.rows), scroll+visible)
	lines := make([]string, 0, end-scroll)
	for i := scroll; i < end; i++ {
		row := d.rows[i]
		marker := "  "
		if i == d.cursor {
			marker = tuiAccentStyle.Render("▸ ")
		}
		selected := "  "
		if row.Current {
			selected = tuiAccentStyle.Render("● ")
		}
		text := marker + selected + row.Name
		if row.Description != "" {
			text += tuiDimStyle.Render(" - " + row.Description)
		}
		lines = append(lines, ansi.Truncate(text, max(1, inner), "…"))
	}
	return lines
}

func (d *agentDialog) ViewAt(w, h int) (string, dialogLayout) {
	l := d.layout(w, h)
	rows := d.rowLines(l.innerW, l.pageH)
	return renderDialogFrame("◇ agents", rows, d.footer(), l), l
}

func (d *agentDialog) footer() string {
	if d.notice != "" {
		return tuiErrorStyle.Render(d.notice)
	}
	if d.busy {
		return tuiDimStyle.Render("finish current work first · esc close")
	}
	return tuiDimStyle.Render("↑↓/j/k move · enter select · esc/q close")
}

func (d *agentDialog) selected() (agentListRow, bool) {
	if d.cursor < 0 || d.cursor >= len(d.rows) {
		return agentListRow{}, false
	}
	return d.rows[d.cursor], true
}

func (d *agentDialog) rowAtY(y int, w, h int) (agentListRow, bool) {
	l := d.layout(w, h)
	local := y - l.rect.y - 1
	if local < 0 || local >= l.pageH {
		return agentListRow{}, false
	}
	index := d.scroll + local
	if index < 0 || index >= len(d.rows) {
		return agentListRow{}, false
	}
	return d.rows[index], true
}

func (m *tuiModel) openAgentDialog() {
	m.closeSuggest()
	m.closeHistory()
	var rows []agentListRow
	if m.agentState != nil {
		rows = agentListRows(m.agentState.Registry, currentAgentName(m.agentState))
	}
	m.agentDlg = newAgentDialog(rows, m.waiting)
	m.hitMap.invalidate()
}

func (m *tuiModel) selectAgentDialogRow(row agentListRow) {
	if m.waiting {
		m.agentDlg.notice = "finish current work first"
		return
	}
	if err := m.switchAgent(row.Name); err != nil {
		m.agentDlg.notice = safeAgentError(err)
		return
	}
	m.agentDlg = nil
	m.hitMap.invalidate()
	m.appendInfo(formatAgentSet(row.Name))
}

func (m *tuiModel) handleAgentDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.agentDlg
	if d == nil {
		return true, true, nil
	}
	layout := d.layout(max(1, m.width), max(1, m.height))
	switch key {
	case "esc", "q":
		m.agentDlg = nil
		m.hitMap.invalidate()
	case "up", "k":
		d.move(-1)
	case "down", "j":
		d.move(1)
	case "home", "g":
		d.cursor = 0
		d.scroll = 0
	case "end", "G":
		if len(d.rows) > 0 {
			d.cursor = len(d.rows) - 1
		}
		d.clampScroll(layout.pageH)
	case "pgup", "b":
		d.move(-max(1, layout.pageH))
	case "pgdown", "f", " ":
		d.move(max(1, layout.pageH))
	case "enter":
		if row, ok := d.selected(); ok {
			m.selectAgentDialogRow(row)
		}
	}
	return true, true, nil
}

func (m *tuiModel) switchAgent(name string) error {
	return applySessionAgent(m.session, m.config, m.agentState, name, m.waiting)
}

func safeAgentError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "active"), strings.Contains(message, "busy"), strings.Contains(message, "finish current"):
		return "finish current work first"
	case strings.Contains(message, "unknown agent"):
		return err.Error()
	case strings.Contains(message, "no agents"):
		return "no agents loaded"
	default:
		return "agent switch failed"
	}
}

func formatAgentUnavailable(err error) string {
	if err == nil {
		return "agent switch failed"
	}
	return err.Error()
}
