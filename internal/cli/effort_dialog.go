package cli

import (
	"errors"
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

func effortDialogPrefs() DialogPrefs {
	// Wider than the row content needs, because the EMPTY state is one long
	// sentence naming the model and it is the only thing that dialog has to
	// say. Truncating it would defeat the reason the dialog opens at all.
	return DialogPrefs{PreferredWPct: 70, PreferredHPct: 50, MinW: 28, MinH: 6, FrameCols: 4, FrameRows: 3, Pager: true}
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

func (d *effortDialog) layout(w, h int) DialogLayout {
	return MakeDialogLayout(w, h, effortDialogPrefs(), func(innerW int) (int, int) {
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
		// Two lines rather than one sentence: the model name is the part the
		// user needs, and a single long line is the first thing a narrow
		// terminal truncates away.
		return []string{
			ansi.Truncate(d.model, max(1, inner), "…"),
			ansi.Truncate(TUIDimStyle.Render("no reasoning effort configured"), max(1, inner), "…"),
		}
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
		text := marker + selected + effortRowName(choice)
		if choice == d.fallback {
			text += TUIDimStyle.Render(" (default)")
		}
		if !choice.Active() {
			// Spelled out because the neighbouring row may be off, and the two
			// are different requests: this one carries no reasoning field.
			text += TUIDimStyle.Render(" · sends no reasoning field")
		}
		lines = append(lines, ansi.Truncate(text, max(1, inner), "…"))
	}
	return lines
}

func (d *effortDialog) ViewAt(w, h int) (string, DialogLayout) {
	l := d.layout(w, h)
	rows := d.rowLines(l.InnerW, l.PageH)
	return renderDialogFrame("◇ reasoning effort", rows, d.footer(), l), l
}

func (d *effortDialog) footer() string {
	if d.notice != "" {
		return tuiErrorStyle.Render(d.notice)
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

func (m *tuiModel) openEffortDialog() {
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

func (m *tuiModel) selectEffortDialogRow(level reasoning.Level) {
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

func (m *tuiModel) handleEffortDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.effortDlg
	if d == nil {
		return true, true, nil
	}
	// A notice describes the session at the moment Enter was refused, and the
	// condition it names can lift while the dialog stays open. The next
	// keystroke is a fresh look, so it does not inherit the old verdict; Enter
	// writes a new one below if the refusal still stands.
	d.notice = ""
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
		d.clampScroll(layout.PageH)
	case "pgup", "b":
		d.move(-max(1, layout.PageH))
	case "pgdown", "f", " ":
		d.move(max(1, layout.PageH))
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

// effortBusyNotice is the single wording for "this dial cannot move yet". The
// picker footer, the typed argument and a session refusal all describe the
// same state, so they say it the same way.
const effortBusyNotice = "finish current work first"

// effortOrchestrationNotice replaces the shared switch guard's wording. That
// guard is written for /model and /agent, and telling someone who typed
// /effort that "model switching is unavailable" names an action they did not
// take - and overflows the 52 columns the dialog footer has at 80 columns.
const effortOrchestrationNotice = "effort is locked while orchestration runs"

// errOrchestrationSwitchActive is orchestrationSwitchGuard's refusal as a
// value, because /effort rewrites it for a surface where "model switching"
// names a command the user did not type. Matching that rewrite on the text
// would go quiet the first time someone copy-edits this sentence, and the
// notice would silently revert.
var errOrchestrationSwitchActive = errors.New("model switching is unavailable while orchestration is active")

// sessionEffortBusyRefusal is chat.Session's wording for an in-flight turn.
// It lives in another package with no sentinel to match, so this surface owns
// a copy of the sentence and TestEffortBusyRefusalMatchesThePickerWording is
// what keeps the copy honest.
const sessionEffortBusyRefusal = "reasoning effort cannot change while work is active"

// safeEffortError keeps the session's own wording where it already names the
// level and the offered set, and rewrites the refusals that were written for
// another command. Unlike a model switch, nothing here can carry a credential
// or a provider message.
func safeEffortError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errOrchestrationSwitchActive) {
		return effortOrchestrationNotice
	}
	if msg := err.Error(); msg != sessionEffortBusyRefusal {
		return msg
	}
	return effortBusyNotice
}

// effortUnsetWord is the one spelling of the unset state: the picker row and
// the typed argument use it, so what the user reads is what the user can type.
const effortUnsetWord = "unset"

// effortRowName names a row. The unset level has no wire spelling of its own,
// so it needs a word here.
func effortRowName(level reasoning.Level) string {
	if !level.Active() {
		return effortUnsetWord
	}
	return string(level)
}

// parseEffortArg reads a /effort argument. It accepts the unset word on top of
// the levels, which is how the text surfaces reach the state reasoning.Level
// spells as empty - reasoning.ParseLevel cannot carry it, because there an
// empty argument is a missing key rather than a request to clear.
func parseEffortArg(arg string) (reasoning.Level, error) {
	if arg == effortUnsetWord {
		return "", nil
	}
	return reasoning.ParseLevel(arg)
}

// formatEffortSet confirms what the request will now carry, which is not the
// same as what was asked for: clearing the override on a model with a
// configured default puts that default back on the wire, and reporting the
// argument there would promise silence the provider never gets.
func formatEffortSet(model string, requested reasoning.Level, effective reasoning.Setting) string {
	switch {
	case requested.Active():
		return fmt.Sprintf("reasoning effort set to %s for %s", effective.Level, model)
	case effective.Active():
		return fmt.Sprintf("reasoning effort choice cleared for %s: %s (model default) is in force",
			model, effective.Level)
	default:
		return fmt.Sprintf("reasoning effort %s for %s: no reasoning field is sent", effortUnsetWord, model)
	}
}

// formatEffortSummary is the no-argument answer on both surfaces: what is
// active now, and what else this model offers.
func formatEffortSummary(model string, choices []reasoning.Level, current, fallback reasoning.Level) string {
	if len(choices) == 0 {
		return fmt.Sprintf("no reasoning effort configured for %s", model)
	}
	line := fmt.Sprintf("reasoning effort=%s for %s (offers %s",
		effortRowName(current), model, reasoning.FormatLevels(choices))
	// The plain surface has no picker, so this line is the only place the unset
	// word is discoverable - but only where unset is a state this model can
	// reach, which is the same question the picker asks before adding its row.
	if effortOffersUnset(choices, fallback) {
		line += ", or " + effortUnsetWord
	}
	return line + ")"
}

// formatEffortStatus is the /status reading of the dial: the level plus the
// dialect that carries it, since the same level reaches different providers as
// different JSON. A model with no reasoning surface says so rather than
// leaving the field blank.
//
// offersReasoning is a separate argument because "has this model anything to
// offer" is a question about its DECLARED SET, which no dialect value answers:
// an absent dialect resolves to the provider's default, and a declared one is a
// wire shape for levels that may not exist. Callers take it from
// Session.ReasoningChoices, the same set /effort accepts against.
func formatEffortStatus(setting reasoning.Setting, offersReasoning bool) string {
	if !offersReasoning {
		return "none · model declares no reasoning efforts"
	}
	if !setting.Active() {
		return effortUnsetWord + " · no reasoning field is sent"
	}
	if setting.Dialect == "" {
		return string(setting.Level)
	}
	return fmt.Sprintf("%s · %s", setting.Level, setting.Dialect)
}

// handleTuiEffortSlash routes /effort. With no argument it opens the picker,
// which is also how a model that offers nothing gets to explain itself. With
// an argument it applies directly, so the command stays scriptable.
func (m *tuiModel) handleTuiEffortSlash(fields []string) bool {
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

// handleSlashEffort is the plain-surface /effort. There is no picker here, so
// the no-argument form prints what the picker would have shown: the active
// level and the set this model offers, or why there is nothing to choose.
func handleSlashEffort(fields []string, sess *chat.Session, term *Terminal) (bool, bool, error) {
	sink := slashSinkFor(term)
	model := sess.CurrentModel()
	if len(fields) < 2 {
		sink.Info(formatEffortSummary(model, sess.ReasoningChoices(), sess.ReasoningEffort(), sess.ReasoningDefault()))
		return true, false, nil
	}
	level, err := parseEffortArg(strings.TrimSpace(fields[1]))
	if err != nil {
		sink.Error(err.Error())
		return true, false, nil
	}
	if err := sess.SetReasoningEffort(level); err != nil {
		sink.Error(safeEffortError(err))
		return true, false, nil
	}
	sink.Info(formatEffortSet(model, level, sess.ReasoningSetting()))
	if jsink, ok := sink.(*jsonSlashSink); ok {
		jsink.EffortChanged(model, level)
	}
	return true, false, nil
}
