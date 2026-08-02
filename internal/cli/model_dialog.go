package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type modelDialogRow struct {
	header     bool
	provider   string
	model      string
	selectable bool
	reason     string
	// effort is the reasoning level this row's annotation states, empty when
	// there is none to state. On the rows the user is choosing between it is the
	// model's CONFIGURED default, because that is what selecting the row would
	// give them. On the current row it is the level actually in force, /effort
	// override included: that row wears the ● marker, so a configured default
	// there would read as a claim about the running session.
	effort string
}

type modelDialog struct {
	rows      []modelDialogRow
	cursor    int
	scroll    int
	notice    string
	busy      bool
	selection chat.Selection
}

// newModelDialog builds the picker. activeEffort is the level the session is
// really running at, which only the row for selection can honestly show.
func newModelDialog(groups []config.ProviderModelGroup, selection chat.Selection, activeEffort string, busy bool) *modelDialog {
	d := &modelDialog{busy: busy, selection: selection}
	for _, group := range groups {
		d.rows = append(d.rows, modelDialogRow{header: true, provider: group.Provider, selectable: group.Selectable, reason: group.DisabledReason})
		for _, model := range group.Models {
			effort := modelDefaultEffort(model)
			if group.Selectable && group.Provider == selection.ProviderName && model.Name == selection.Model {
				effort = activeEffort
			}
			d.rows = append(d.rows, modelDialogRow{
				provider: group.Provider, model: model.Name,
				selectable: group.Selectable, reason: group.DisabledReason,
				effort: effort,
			})
		}
	}
	for i, row := range d.rows {
		if !row.header && row.provider == selection.ProviderName && row.model == selection.Model {
			d.cursor = i
			break
		}
	}
	if d.cursor == 0 && (len(d.rows) == 0 || d.rows[0].header) {
		d.cursor = d.nextModel(0, 1)
	}
	return d
}

func modelDialogPrefs() dialogPrefs {
	return dialogPrefs{preferredWPct: 80, preferredHPct: 75, minW: 32, minH: 8, frameCols: 4, frameRows: 3, pager: true}
}

func (d *modelDialog) nextModel(from, direction int) int {
	for i := from; i >= 0 && i < len(d.rows); i += direction {
		if !d.rows[i].header {
			return i
		}
	}
	return 0
}

func (d *modelDialog) move(delta int) {
	if len(d.rows) == 0 || delta == 0 {
		return
	}
	direction := 1
	if delta < 0 {
		direction = -1
	}
	for n := 0; n < absInt(delta); n++ {
		next := d.nextModel(d.cursor+direction, direction)
		if next == 0 && d.cursor != 0 || next == d.cursor {
			break
		}
		d.cursor = next
	}
	d.clampScroll(1)
}

func (d *modelDialog) clampScroll(page int) {
	page = max(1, page)
	if d.cursor < d.scroll {
		d.scroll = d.cursor
	}
	if d.cursor >= d.scroll+page {
		d.scroll = d.cursor - page + 1
	}
	d.scroll = max(0, min(d.scroll, max(0, len(d.rows)-page)))
}

func (d *modelDialog) layout(w, h int) dialogLayout {
	return makeDialogLayout(w, h, modelDialogPrefs(), func(innerW int) (int, int) {
		// Measurement must not clamp the live scroll position. This layout is
		// also used by mouse hit-testing, so mutating scroll here can make a
		// click resolve against a different page than the one rendered.
		rows := d.rowLinesAt(innerW, len(d.rows), 0)
		maxW := 0
		for _, row := range rows {
			maxW = max(maxW, ansi.StringWidth(row))
		}
		return maxW, len(rows)
	})
}

func (d *modelDialog) rowLines(inner, visible int) []string {
	visible = max(1, visible)
	d.clampScroll(visible)
	return d.rowLinesAt(inner, visible, d.scroll)
}

func (d *modelDialog) rowLinesAt(inner, visible, scroll int) []string {
	visible = max(1, visible)
	if len(d.rows) == 0 {
		return []string{tuiDimStyle.Render("no configured models")}
	}
	scroll = max(0, min(scroll, max(0, len(d.rows)-visible)))
	end := min(len(d.rows), scroll+visible)
	lines := make([]string, 0, end-scroll)
	for i := scroll; i < end; i++ {
		row := d.rows[i]
		if row.header {
			text := "◆ " + row.provider
			if row.reason != "" {
				text += " · " + row.reason
			}
			lines = append(lines, tuiHeaderStyle.Render(text))
			continue
		}
		marker := "  "
		if i == d.cursor {
			marker = tuiAccentStyle.Render("▸ ")
		}
		selected := "  "
		if row.provider == d.selection.ProviderName && row.model == d.selection.Model && row.selectable {
			selected = tuiAccentStyle.Render("● ")
		}
		text := marker + selected + row.model
		// Reasoning is per model precisely so the user can see it at selection
		// time. A model that offers nothing shows nothing rather than a column
		// of "none" on catalogs that use no reasoning at all.
		if row.effort != "" {
			text += tuiDimStyle.Render("  effort: " + row.effort)
		}
		if !row.selectable {
			text = tuiDimStyle.Render(text)
		}
		lines = append(lines, ansi.Truncate(text, max(1, inner), "…"))
	}
	return lines
}

func (d *modelDialog) ViewAt(w, h int) (string, dialogLayout) {
	l := d.layout(w, h)
	rows := d.rowLines(l.innerW, l.pageH)
	return renderDialogFrame("◇ models", rows, d.footer(), l), l
}

func (d *modelDialog) footer() string {
	if d.notice != "" {
		return tuiErrorStyle.Render(d.notice)
	}
	if d.busy {
		return tuiDimStyle.Render("finish current work first · esc close")
	}
	return tuiDimStyle.Render("↑↓/j/k move · home/end · pgup/pgdn · enter select · esc/q close")
}

func (d *modelDialog) rowAtY(y int, w, h int) (modelDialogRow, bool) {
	l := d.layout(w, h)
	local := y - l.rect.y - 1
	if local < 0 || local >= l.pageH {
		return modelDialogRow{}, false
	}
	index := d.scroll + local
	if index < 0 || index >= len(d.rows) {
		return modelDialogRow{}, false
	}
	return d.rows[index], true
}

func (d *modelDialog) selected() (modelDialogRow, bool) {
	if d.cursor < 0 || d.cursor >= len(d.rows) || d.rows[d.cursor].header {
		return modelDialogRow{}, false
	}
	return d.rows[d.cursor], true
}

func (m *tuiModel) openModelDialog() {
	m.closeSuggest()
	var groups []config.ProviderModelGroup
	if m.config != nil {
		groups = m.config.ModelCatalog()
	}
	if len(groups) == 0 && m.config != nil {
		groups = []config.ProviderModelGroup{{Provider: m.config.ProviderName, Models: m.config.ModelProfiles, Active: true, Selectable: true}}
		if len(groups[0].Models) == 0 {
			for _, name := range m.config.Models {
				groups[0].Models = append(groups[0].Models, config.ModelSpec{Name: name})
			}
		}
	}
	m.modelDlg = newModelDialog(groups, m.session.CurrentSelection(), string(m.session.ReasoningEffort()), m.waiting)
	m.hitMap.invalidate()
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (m *tuiModel) selectModelDialogRow(row modelDialogRow) {
	if !row.selectable {
		m.modelDlg.notice = "model is unavailable: " + row.reason
		return
	}
	if m.waiting {
		m.modelDlg.notice = "finish current work first"
		return
	}
	// A model change drops the /effort choice made for the outgoing model, and
	// the transcript is the only place that can witness it.
	discarded, err := m.switchModel(row.provider, row.model)
	if err != nil {
		m.modelDlg.notice = safeModelError(err)
		return
	}
	m.modelDlg = nil
	m.hitMap.invalidate()
	m.modelName = shortenModel(m.session.CurrentModel())
	m.appendInfo(fmt.Sprintf("model set to %s/%s", row.provider, row.model) + effortDiscardedSuffix(discarded))
}

func (m *tuiModel) handleModelDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.modelDlg
	if d == nil {
		return true, true, nil
	}
	layout := d.layout(max(1, m.width), max(1, m.height))
	switch key {
	case "esc", "q":
		m.modelDlg = nil
		m.hitMap.invalidate()
	case "up", "k":
		d.move(-1)
	case "down", "j":
		d.move(1)
	case "home", "g":
		d.cursor = d.nextModel(0, 1)
		d.scroll = 0
	case "end", "G":
		d.cursor = d.nextModel(len(d.rows)-1, -1)
		d.clampScroll(layout.pageH)
	case "pgup", "b":
		d.move(-max(1, layout.pageH))
	case "pgdown", "f", " ":
		d.move(max(1, layout.pageH))
	case "enter":
		if row, ok := d.selected(); ok {
			m.selectModelDialogRow(row)
		}
	}
	return true, true, nil
}

func safeModelError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "active"), strings.Contains(message, "busy"):
		return "finish current work first"
	case strings.Contains(message, "credential"), strings.Contains(message, "api key"):
		return "provider credentials unavailable"
	case strings.Contains(message, "not configured"), strings.Contains(message, "not selectable"), strings.Contains(message, "invalid"):
		return "model is not configured"
	default:
		return "model switch failed"
	}
}

func (m *tuiModel) switchModel(providerName, model string) (reasoning.Level, error) {
	// m.config is set at TUI construction (newTUIModel); switchModelCommand
	// already rejects a nil config via buildModelBinding when needed.
	return switchModelCommand(m.session, m.config, providerName, model)
}

// modelDefaultEffort renders a catalog entry's default reasoning level for the
// picker. A model that declares no efforts, or declares some but ships with
// none active, renders nothing: the annotation is there to say what WILL be
// sent, and in both cases that is nothing.
func modelDefaultEffort(spec config.ModelSpec) string {
	if !config.ModelOffersReasoning(spec) || !spec.Reasoning.Active() {
		return ""
	}
	return string(spec.Reasoning)
}
