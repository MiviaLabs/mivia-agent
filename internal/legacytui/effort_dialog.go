package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"

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

// effortRowsWithUnset gives a model that declares efforts but no configured
// default a row for the state it shipped in: no reasoning field sent at all.
// Without it, picking any level is one-way, since nothing else in the picker
// spells "send nothing" - off is a level a model may declare, and it sends an
// explicit disable instead.
//
// A model that HAS a configured default needs no such row: its default is
// already one of the declared levels, and the (default) label marks it.
func effortRowsWithUnset(choices []reasoning.Level, fallback reasoning.Level) []reasoning.Level {
	if !effortOffersUnset(choices, fallback) {
		return choices
	}
	return append([]reasoning.Level{""}, choices...)
}

// effortOffersUnset answers, once, whether unset is a state this model can be
// returned to. The picker row and the summary line both need the answer, and
// deciding it twice is how the summary came to recommend a no-op the picker
// refused to show.
func effortOffersUnset(choices []reasoning.Level, fallback reasoning.Level) bool {
	return len(choices) > 0 && !fallback.Active()
}

func newEffortDialog(model string, choices []reasoning.Level, current, fallback reasoning.Level, busy bool) *effortDialog {
	choices = effortRowsWithUnset(choices, fallback)
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

func effortDialogPrefs() cli.DialogPrefs {
	// Wider than the row content needs, because the EMPTY state is one long
	// sentence naming the model and it is the only thing that dialog has to
	// say. Truncating it would defeat the reason the dialog opens at all.
	return cli.DialogPrefs{PreferredWPct: 70, PreferredHPct: 50, MinW: 28, MinH: 6, FrameCols: 4, FrameRows: 3, Pager: true}
}

func (d *effortDialog) move(delta int) {
	if d.offersNothing() || delta == 0 {
		return
	}
	d.cursor = cli.Max(0, cli.Min(d.cursor+delta, len(d.choices)-1))
	d.clampScroll(1)
}

func (d *effortDialog) clampScroll(page int) {
	page = cli.Max(1, page)
	if d.cursor < d.scroll {
		d.scroll = d.cursor
	}
	if d.cursor >= d.scroll+page {
		d.scroll = d.cursor - page + 1
	}
	d.scroll = cli.Max(0, cli.Min(d.scroll, cli.Max(0, len(d.choices)-page)))
}

func (d *effortDialog) layout(w, h int) cli.DialogLayout {
	return cli.MakeDialogLayout(w, h, effortDialogPrefs(), func(innerW int) (int, int) {
		rows := d.rowLinesAt(innerW, cli.Max(1, len(d.choices)), 0)
		maxW := 0
		for _, row := range rows {
			maxW = cli.Max(maxW, ansi.StringWidth(row))
		}
		return maxW, len(rows)
	})
}

func (d *effortDialog) rowLines(inner, visible int) []string {
	visible = cli.Max(1, visible)
	d.clampScroll(visible)
	return d.rowLinesAt(inner, visible, d.scroll)
}

func (d *effortDialog) rowLinesAt(inner, visible, scroll int) []string {
	visible = cli.Max(1, visible)
	if d.offersNothing() {
		// Two lines rather than one sentence: the model name is the part the
		// user needs, and a single long line is the first thing a narrow
		// terminal truncates away.
		return []string{
			ansi.Truncate(d.model, cli.Max(1, inner), "…"),
			ansi.Truncate(TUIDimStyle.Render("no reasoning effort configured"), cli.Max(1, inner), "…"),
		}
	}
	scroll = cli.Max(0, cli.Min(scroll, cli.Max(0, len(d.choices)-visible)))
	end := cli.Min(len(d.choices), scroll+visible)
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
		text := marker + selected + effortRowName(choice)
		if choice == d.fallback {
			text += TUIDimStyle.Render(" (default)")
		}
		if !choice.Active() {
			// Spelled out because the neighbouring row may be off, and the two
			// are different requests: this one carries no reasoning field.
			text += TUIDimStyle.Render(" · sends no reasoning field")
		}
		lines = append(lines, ansi.Truncate(text, cli.Max(1, inner), "…"))
	}
	return lines
}

func (d *effortDialog) ViewAt(w, h int) (string, cli.DialogLayout) {
	l := d.layout(w, h)
	rows := d.rowLines(l.InnerW, l.PageH)
	return cli.RenderDialogFrame("◇ reasoning effort", rows, d.footer(), l), l
}

func (d *effortDialog) footer() string {
	if d.notice != "" {
		return TUIErrorStyle.Render(d.notice)
	}
	if d.offersNothing() {
		return TUIDimStyle.Render("declare reasoning_efforts in mivia.toml · esc close")
	}
	if d.busy {
		return TUIDimStyle.Render(effortBusyNotice + " · esc close")
	}
	return TUIDimStyle.Render("↑↓/j/k move · enter select · esc/q close")
}

func (d *effortDialog) selected() (reasoning.Level, bool) {
	if d.offersNothing() || d.cursor < 0 || d.cursor >= len(d.choices) {
		return "", false
	}
	return d.choices[d.cursor], true
}

func (m *TUIModel) openEffortDialog() {
	m.closeSuggest()
	m.closeHistory()
	m.effortDlg = newEffortDialog(
		m.session.CurrentModel(),
		m.session.ReasoningChoices(),
		m.session.ReasoningEffort(),
		m.session.ReasoningDefault(),
		m.waiting,
	)
	m.hitMap.invalidate()
}

func (m *TUIModel) selectEffortDialogRow(level reasoning.Level) {
	if m.waiting {
		m.effortDlg.notice = effortBusyNotice
		return
	}
	if err := m.session.SetReasoningEffort(level); err != nil {
		m.effortDlg.notice = safeEffortError(err)
		return
	}
	m.effortDlg = nil
	m.hitMap.invalidate()
	m.appendInfo(formatEffortSet(m.session.CurrentModel(), level, m.session.ReasoningSetting()))
}

func (m *TUIModel) handleEffortDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.effortDlg
	if d == nil {
		return true, true, nil
	}
	// A notice describes the session at the moment Enter was refused, and the
	// condition it names can lift while the dialog stays open. The next
	// keystroke is a fresh look, so it does not inherit the old verdict; Enter
	// writes a new one below if the refusal still stands.
	d.notice = ""
	layout := d.layout(cli.Max(1, m.width), cli.Max(1, m.height))
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
		d.clampScroll(layout.PageH)
	case "pgup", "b":
		d.move(-cli.Max(1, layout.PageH))
	case "pgdown", "f", " ":
		d.move(cli.Max(1, layout.PageH))
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

// effortBusyNotice, effortOrchestrationNotice, ErrOrchestrationSwitchActive,
// sessionEffortBusyRefusal, safeEffortError, effortUnsetWord, effortRowName,
// parseEffortArg, formatEffortSet, formatEffortSummary, FormatEffortStatus,
// HandleSlashEffort are relocated to internal/cli (needed there by the
// classic-mode /effort handler, and ErrOrchestrationSwitchActive's true
// producer, cli's own orchestrationSwitchGuard, already lives there); aliased
// here so this package's own call sites are unchanged.
const (
	effortBusyNotice          = cli.EffortBusyNotice
	effortOrchestrationNotice = cli.EffortOrchestrationNotice
	sessionEffortBusyRefusal  = cli.SessionEffortBusyRefusal
	effortUnsetWord           = cli.EffortUnsetWord
)

var (
	ErrOrchestrationSwitchActive = cli.ErrOrchestrationSwitchActive
	safeEffortError              = cli.SafeEffortError
	effortRowName                = cli.EffortRowName
	parseEffortArg               = cli.ParseEffortArg
	formatEffortSet              = cli.FormatEffortSet
	formatEffortSummary          = cli.FormatEffortSummary
	FormatEffortStatus           = cli.FormatEffortStatus
	HandleSlashEffort            = cli.HandleSlashEffort
)

// handleTuiEffortSlash routes /effort. With no argument it opens the picker,
// which is also how a model that offers nothing gets to explain itself. With
// an argument it applies directly, so the command stays scriptable.
func (m *TUIModel) handleTuiEffortSlash(fields []string) bool {
	if len(fields) < 2 {
		m.openEffortDialog()
		return true
	}
	// The session refuses a busy change on its own; this is about the wording,
	// which must match what the picker and /budget say for the same state.
	if m.waiting {
		m.appendInfo(effortBusyNotice)
		return true
	}
	level, err := parseEffortArg(strings.TrimSpace(fields[1]))
	if err != nil {
		m.appendInfo(err.Error())
		return true
	}
	if err := m.session.SetReasoningEffort(level); err != nil {
		m.appendInfo(safeEffortError(err))
		return true
	}
	m.appendInfo(formatEffortSet(m.session.CurrentModel(), level, m.session.ReasoningSetting()))
	return true
}
