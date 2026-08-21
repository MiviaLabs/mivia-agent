package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
)

// agentListRows builds ordered rows from a registry. Pure; unit-tested without TUI.
func agentListRows(reg *agents.AgentRegistry, current string) []cli.AgentListRow {
	if reg == nil {
		return nil
	}
	current = strings.TrimSpace(current)
	names := reg.Names()
	out := make([]cli.AgentListRow, 0, len(names))
	for _, name := range names {
		a, ok := reg.Get(name)
		if !ok {
			continue
		}
		out = append(out, cli.AgentListRow{
			Name:        a.Name,
			Description: a.Description,
			Current:     a.Name == current,
		})
	}
	return out
}

type agentDialog struct {
	rows   []cli.AgentListRow
	cursor int
	scroll int
	notice string
	busy   bool
}

func newAgentDialog(rows []cli.AgentListRow, busy bool) *agentDialog {
	d := &agentDialog{rows: rows, busy: busy}
	for i, row := range rows {
		if row.Current {
			d.cursor = i
			break
		}
	}
	return d
}

func agentDialogPrefs() cli.DialogPrefs {
	return cli.DialogPrefs{PreferredWPct: 70, PreferredHPct: 60, MinW: 28, MinH: 8, FrameCols: 4, FrameRows: 3, Pager: true}
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
	page = cli.Max(1, page)
	if d.cursor < d.scroll {
		d.scroll = d.cursor
	}
	if d.cursor >= d.scroll+page {
		d.scroll = d.cursor - page + 1
	}
	d.scroll = cli.Max(0, cli.Min(d.scroll, cli.Max(0, len(d.rows)-page)))
}

func (d *agentDialog) layout(w, h int) cli.DialogLayout {
	return cli.MakeDialogLayout(w, h, agentDialogPrefs(), func(innerW int) (int, int) {
		rows := d.rowLinesAt(innerW, len(d.rows), 0)
		maxW := 0
		for _, row := range rows {
			maxW = cli.Max(maxW, ansi.StringWidth(row))
		}
		return maxW, len(rows)
	})
}

func (d *agentDialog) rowLines(inner, visible int) []string {
	visible = cli.Max(1, visible)
	d.clampScroll(visible)
	return d.rowLinesAt(inner, visible, d.scroll)
}

func (d *agentDialog) rowLinesAt(inner, visible, scroll int) []string {
	visible = cli.Max(1, visible)
	if len(d.rows) == 0 {
		return []string{TUIDimStyle.Render("no agents loaded")}
	}
	scroll = cli.Max(0, cli.Min(scroll, cli.Max(0, len(d.rows)-visible)))
	end := cli.Min(len(d.rows), scroll+visible)
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
			text += TUIDimStyle.Render(" - " + row.Description)
		}
		lines = append(lines, ansi.Truncate(text, cli.Max(1, inner), "…"))
	}
	return lines
}

func (d *agentDialog) ViewAt(w, h int) (string, cli.DialogLayout) {
	l := d.layout(w, h)
	rows := d.rowLines(l.InnerW, l.PageH)
	return cli.RenderDialogFrame("◇ agents", rows, d.footer(), l), l
}

func (d *agentDialog) footer() string {
	if d.notice != "" {
		return TUIErrorStyle.Render(d.notice)
	}
	if d.busy {
		return TUIDimStyle.Render("finish current work first · esc close")
	}
	return TUIDimStyle.Render("↑↓/j/k move · enter select · esc/q close")
}

func (d *agentDialog) selected() (cli.AgentListRow, bool) {
	if d.cursor < 0 || d.cursor >= len(d.rows) {
		return cli.AgentListRow{}, false
	}
	return d.rows[d.cursor], true
}

func (d *agentDialog) rowAtY(y int, w, h int) (cli.AgentListRow, bool) {
	l := d.layout(w, h)
	local := y - l.Rect.Y - 1
	if local < 0 || local >= l.PageH {
		return cli.AgentListRow{}, false
	}
	index := d.scroll + local
	if index < 0 || index >= len(d.rows) {
		return cli.AgentListRow{}, false
	}
	return d.rows[index], true
}

func (m *TUIModel) openAgentDialog() {
	m.closeSuggest()
	m.closeHistory()
	var rows []cli.AgentListRow
	if m.agentState != nil {
		rows = agentListRows(m.agentState.Registry, cli.CurrentAgentName(m.agentState))
	}
	m.agentDlg = newAgentDialog(rows, m.waiting)
	m.hitMap.invalidate()
}

func (m *TUIModel) selectAgentDialogRow(row cli.AgentListRow) {
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
	m.appendInfo(cli.FormatAgentSet(row.Name))
}

func (m *TUIModel) handleAgentDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.agentDlg
	if d == nil {
		return true, true, nil
	}
	layout := d.layout(cli.Max(1, m.width), cli.Max(1, m.height))
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
		d.clampScroll(layout.PageH)
	case "pgup", "b":
		d.move(-cli.Max(1, layout.PageH))
	case "pgdown", "f", " ":
		d.move(cli.Max(1, layout.PageH))
	case "enter":
		if row, ok := d.selected(); ok {
			m.selectAgentDialogRow(row)
		}
	}
	return true, true, nil
}

func (m *TUIModel) switchAgent(name string) error {
	return cli.ApplySessionAgent(m.session, m.config, m.agentState, name, m.waiting)
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

// FormatAgentUnavailable is relocated to internal/cli (needed there by the
// classic-mode slash-command handlers); aliased here so this package's own
// call sites are unchanged.
var FormatAgentUnavailable = cli.FormatAgentUnavailable
