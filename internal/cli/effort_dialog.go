package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// effortDialog is the /effort picker: the reasoning levels the ACTIVE model
// declared, in the order its configuration lists them.
//
// A model that declares nothing still opens the dialog. Showing an empty list
// would leave the user guessing whether the feature is broken or the model
// simply has no reasoning surface, so the empty state says which, by name.
type effortDialog struct {
	model    string
	choices  []reasoning.Level
	current  reasoning.Level
	fallback reasoning.Level // the model's configured default
	cursor   int
	scroll   int
	notice   string
	busy     bool
}

func newEffortDialog(model string, choices []reasoning.Level, current, fallback reasoning.Level, busy bool) *effortDialog {
	d := &effortDialog{model: model, choices: choices, current: current, fallback: fallback, busy: busy}
	for i, choice := range choices {
		if choice == current {
			d.cursor = i
			break
		}
	}
	return d
}

// offersNothing reports the empty state. It is asked in three places - render,
// Enter, and the footer - so it is a predicate rather than a repeated len().
func (d *effortDialog) offersNothing() bool { return len(d.choices) == 0 }

func effortDialogPrefs() dialogPrefs {
	return dialogPrefs{preferredWPct: 60, preferredHPct: 50, minW: 28, minH: 6, frameCols: 4, frameRows: 3, pager: true}
}

func (d *effortDialog) move(delta int) {
	if d.offersNothing() || delta == 0 {
		return
	}
	d.cursor = max(0, min(d.cursor+delta, len(d.choices)-1))
	d.clampScroll(1)
}

func (d *effortDialog) clampScroll(page int) {
	page = max(1, page)
	if d.cursor < d.scroll {
		d.scroll = d.cursor
	}
	if d.cursor >= d.scroll+page {
		d.scroll = d.cursor - page + 1
	}
	d.scroll = max(0, min(d.scroll, max(0, len(d.choices)-page)))
}

func (d *effortDialog) layout(w, h int) dialogLayout {
	return makeDialogLayout(w, h, effortDialogPrefs(), func(innerW int) (int, int) {
		rows := d.rowLinesAt(innerW, max(1, len(d.choices)), 0)
		maxW := 0
		for _, row := range rows {
			maxW = max(maxW, ansi.StringWidth(row))
		}
		return maxW, len(rows)
	})
}

func (d *effortDialog) rowLines(inner, visible int) []string {
	visible = max(1, visible)
	d.clampScroll(visible)
	return d.rowLinesAt(inner, visible, d.scroll)
}

func (d *effortDialog) rowLinesAt(inner, visible, scroll int) []string {
	visible = max(1, visible)
	if d.offersNothing() {
		return []string{ansi.Truncate(
			tuiDimStyle.Render("no reasoning effort configured for "+d.model),
			max(1, inner), "…")}
	}
	scroll = max(0, min(scroll, max(0, len(d.choices)-visible)))
	end := min(len(d.choices), scroll+visible)
	lines := make([]string, 0, end-scroll)
	for i := scroll; i < end; i++ {
		choice := d.choices[i]
		marker := "  "
		if i == d.cursor {
			marker = tuiAccentStyle.Render("▸ ")
		}
		selected := "  "
		if choice == d.current {
			selected = tuiAccentStyle.Render("● ")
		}
		text := marker + selected + string(choice)
		if choice == d.fallback {
			text += tuiDimStyle.Render(" (default)")
		}
		lines = append(lines, ansi.Truncate(text, max(1, inner), "…"))
	}
	return lines
}

func (d *effortDialog) ViewAt(w, h int) (string, dialogLayout) {
	l := d.layout(w, h)
	rows := d.rowLines(l.innerW, l.pageH)
	return renderDialogFrame("◇ reasoning effort", rows, d.footer(), l), l
}

func (d *effortDialog) footer() string {
	if d.notice != "" {
		return tuiErrorStyle.Render(d.notice)
	}
	if d.offersNothing() {
		return tuiDimStyle.Render("declare reasoning_efforts on this model in mivia.toml · esc close")
	}
	if d.busy {
		return tuiDimStyle.Render("finish current work first · esc close")
	}
	return tuiDimStyle.Render("↑↓/j/k move · enter select · esc/q close")
}

func (d *effortDialog) selected() (reasoning.Level, bool) {
	if d.offersNothing() || d.cursor < 0 || d.cursor >= len(d.choices) {
		return "", false
	}
	return d.choices[d.cursor], true
}

func (m *tuiModel) openEffortDialog() {
	m.closeSuggest()
	m.effortDlg = newEffortDialog(
		m.session.CurrentModel(),
		m.session.ReasoningChoices(),
		m.session.ReasoningEffort(),
		m.session.ReasoningDefault(),
		m.waiting,
	)
	m.hitMap.invalidate()
}

func (m *tuiModel) selectEffortDialogRow(level reasoning.Level) {
	if m.waiting {
		m.effortDlg.notice = "finish current work first"
		return
	}
	if err := m.session.SetReasoningEffort(level); err != nil {
		m.effortDlg.notice = safeEffortError(err)
		return
	}
	m.effortDlg = nil
	m.hitMap.invalidate()
	m.appendInfo(formatEffortSet(m.session.CurrentModel(), level))
}

func (m *tuiModel) handleEffortDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.effortDlg
	if d == nil {
		return true, true, nil
	}
	layout := d.layout(max(1, m.width), max(1, m.height))
	switch key {
	case "esc", "q":
		m.effortDlg = nil
		m.hitMap.invalidate()
	case "up", "k":
		d.move(-1)
	case "down", "j":
		d.move(1)
	case "home", "g":
		d.cursor = 0
		d.scroll = 0
	case "end", "G":
		if !d.offersNothing() {
			d.cursor = len(d.choices) - 1
		}
		d.clampScroll(layout.pageH)
	case "pgup", "b":
		d.move(-max(1, layout.pageH))
	case "pgdown", "f", " ":
		d.move(max(1, layout.pageH))
	case "enter":
		// Enter on the empty state is inert on purpose: there is nothing to
		// choose, and closing would hide the explanation the user opened this
		// dialog to read.
		if level, ok := d.selected(); ok {
			m.selectEffortDialogRow(level)
		}
	}
	return true, true, nil
}

// safeEffortError keeps the session's own wording, which already names the
// level and the offered set. Unlike a model switch, nothing here can carry a
// credential or a provider message.
func safeEffortError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func formatEffortSet(model string, level reasoning.Level) string {
	return fmt.Sprintf("reasoning effort set to %s for %s", level, model)
}

// formatEffortSummary is the no-argument answer on both surfaces: what is
// active now, and what else this model offers.
func formatEffortSummary(model string, choices []reasoning.Level, current reasoning.Level) string {
	if len(choices) == 0 {
		return fmt.Sprintf("no reasoning effort configured for %s", model)
	}
	active := "none"
	if current.Active() {
		active = string(current)
	}
	return fmt.Sprintf("reasoning effort=%s for %s (offers %s)", active, model, reasoning.FormatLevels(choices))
}

// handleTuiEffortSlash routes /effort. With no argument it opens the picker,
// which is also how a model that offers nothing gets to explain itself. With
// an argument it applies directly, so the command stays scriptable.
func (m *tuiModel) handleTuiEffortSlash(fields []string) bool {
	if len(fields) < 2 {
		m.openEffortDialog()
		return true
	}
	level, err := reasoning.ParseLevel(strings.TrimSpace(fields[1]))
	if err != nil {
		m.appendInfo(err.Error())
		return true
	}
	if err := m.session.SetReasoningEffort(level); err != nil {
		m.appendInfo(err.Error())
		return true
	}
	m.appendInfo(formatEffortSet(m.session.CurrentModel(), level))
	return true
}

// handleSlashEffort is the plain-surface /effort. There is no picker here, so
// the no-argument form prints what the picker would have shown: the active
// level and the set this model offers, or why there is nothing to choose.
func handleSlashEffort(fields []string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	sink := terminalSlashSink{t: term}
	model := sess.CurrentModel()
	if len(fields) < 2 {
		sink.Info(formatEffortSummary(model, sess.ReasoningChoices(), sess.ReasoningEffort()))
		return true, false, nil
	}
	level, err := reasoning.ParseLevel(strings.TrimSpace(fields[1]))
	if err != nil {
		sink.Info(err.Error())
		return true, false, nil
	}
	if err := sess.SetReasoningEffort(level); err != nil {
		sink.Info(err.Error())
		return true, false, nil
	}
	sink.Info(formatEffortSet(model, level))
	return true, false, nil
}
