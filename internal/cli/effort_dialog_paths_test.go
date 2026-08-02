package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func newEffortSessionForPlain(t *testing.T) *chat.Session {
	t.Helper()
	return chat.NewSession(effortCatalogConfig(), welcomeStubCompleter{})
}

// A picker taller than its canvas has to page, and its cursor has to stay on
// screen. These drive every navigation branch against a list that cannot fit.
func manyEffortsDialog() *effortDialog {
	choices := []reasoning.Level{
		reasoning.Off, reasoning.Minimal, reasoning.Low,
		reasoning.Medium, reasoning.High, reasoning.XHigh, reasoning.Max,
	}
	return newEffortDialog("big-model", choices, reasoning.Low, reasoning.Low, false)
}

func TestEffortDialogNavigationStaysInRange(t *testing.T) {
	d := manyEffortsDialog()
	d.move(-100)
	if d.cursor != 0 {
		t.Fatalf("cursor = %d after moving far up, want 0", d.cursor)
	}
	d.move(100)
	if d.cursor != len(d.choices)-1 {
		t.Fatalf("cursor = %d after moving far down, want %d", d.cursor, len(d.choices)-1)
	}
	// A zero delta is a no-op rather than a clamp, so a stray key cannot move
	// the cursor.
	before := d.cursor
	d.move(0)
	if d.cursor != before {
		t.Fatalf("a zero move changed the cursor to %d", d.cursor)
	}
}

func TestEffortDialogPagingKeepsTheCursorVisible(t *testing.T) {
	d := manyEffortsDialog()
	const page = 3
	d.cursor = len(d.choices) - 1
	d.clampScroll(page)
	if d.cursor < d.scroll || d.cursor >= d.scroll+page {
		t.Fatalf("cursor %d outside window [%d,%d)", d.cursor, d.scroll, d.scroll+page)
	}
	d.cursor = 0
	d.clampScroll(page)
	if d.scroll != 0 {
		t.Fatalf("scroll = %d after returning to the top", d.scroll)
	}
	lines := d.rowLines(40, page)
	if len(lines) != page {
		t.Fatalf("rendered %d lines for a %d-row page", len(lines), page)
	}
}

func TestEffortDialogKeysDriveTheCursor(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.width, m.height = 90, 24
	m.handleSlash("/effort")
	d := m.effortDlg
	for _, key := range []string{"down", "j", "up", "k", "end", "G", "home", "g", "pgdown", "pgup", "f", "b", " "} {
		m.handleEffortDialogKey(key)
		if d.cursor < 0 || d.cursor >= len(d.choices) {
			t.Fatalf("key %q left cursor at %d", key, d.cursor)
		}
	}
	m.handleEffortDialogKey("esc")
	if m.effortDlg != nil {
		t.Fatal("esc must close the dialog")
	}
	// A key routed to a closed dialog must be inert, not a nil dereference.
	m.handleEffortDialogKey("enter")
}

// An empty picker must survive every navigation key: there is nothing to move
// to, and a stray index here would panic on the next render.
func TestEmptyEffortDialogSurvivesNavigation(t *testing.T) {
	m := effortTUI(t, effortPlain)
	m.width, m.height = 90, 24
	m.handleSlash("/effort")
	for _, key := range []string{"down", "up", "end", "home", "pgdown", "pgup", "enter"} {
		m.handleEffortDialogKey(key)
	}
	if m.effortDlg == nil {
		t.Fatal("navigation closed the empty dialog")
	}
	if m.effortDlg.cursor != 0 {
		t.Fatalf("cursor = %d on an empty picker", m.effortDlg.cursor)
	}
	view, _ := m.effortDlg.ViewAt(90, 24)
	if !strings.Contains(stripANSI(view), "reasoning_efforts") {
		t.Fatalf("empty footer must point at the config key:\n%s", stripANSI(view))
	}
}

// A turn in flight already captured its binding, so the picker refuses rather
// than reporting a change the running request did not get.
func TestEffortDialogRefusesWhileBusy(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.width, m.height = 90, 24
	m.handleSlash("/effort")
	m.waiting = true
	m.handleEffortDialogKey("enter")
	if m.effortDlg == nil {
		t.Fatal("a refused selection must keep the dialog open")
	}
	if m.effortDlg.notice == "" {
		t.Fatal("a refused selection must explain itself")
	}
	view, _ := m.effortDlg.ViewAt(90, 24)
	if !strings.Contains(stripANSI(view), "finish current work") {
		t.Fatalf("notice not rendered:\n%s", stripANSI(view))
	}
}

// The busy footer is a distinct state from the notice footer: it warns before
// the user tries, rather than after.
func TestEffortDialogBusyFooter(t *testing.T) {
	d := newEffortDialog("m", []reasoning.Level{reasoning.High}, reasoning.High, reasoning.High, true)
	if !strings.Contains(stripANSI(d.footer()), "finish current work") {
		t.Fatalf("busy footer = %q", stripANSI(d.footer()))
	}
}

// A selection the session refuses must surface the session's own wording,
// which already names the level and the offered set.
func TestEffortDialogSurfacesASessionRefusal(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.handleSlash("/effort")
	m.effortDlg.choices = append(m.effortDlg.choices, reasoning.XHigh)
	m.effortDlg.cursor = len(m.effortDlg.choices) - 1
	m.handleEffortDialogKey("enter")
	if m.effortDlg == nil {
		t.Fatal("a refused selection must keep the dialog open")
	}
	if !strings.Contains(m.effortDlg.notice, "xhigh") {
		t.Fatalf("notice = %q, want it to name the refused level", m.effortDlg.notice)
	}
	if safeEffortError(nil) != "" {
		t.Fatal("a nil error must render as no notice")
	}
}

// The dialog renders inside the chat view and survives a resize clamp, which
// is where a modal that forgot to register would blow past its rect.
func TestEffortDialogRendersInTheChatViewAndClamps(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.width, m.height = 80, 20
	m.handleSlash("/effort")
	if !strings.Contains(stripANSI(m.renderChatView()), "reasoning effort") {
		t.Fatal("the dialog is not composited into the chat view")
	}
	m.width, m.height = 30, 8
	m.clampModalState()
	view, layout := m.effortDlg.ViewAt(m.width, m.height)
	if layout.rect.x+layout.rect.w > m.width || layout.rect.y+layout.rect.h > m.height {
		t.Fatalf("clamped layout escapes the canvas: %+v", layout)
	}
	if view == "" {
		t.Fatal("empty view after clamp")
	}
}

// Key routing must reach the dialog rather than the composer while it is open.
func TestEffortDialogOwnsKeyRouting(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.width, m.height = 90, 24
	m.handleSlash("/effort")
	// The cursor opens on the effective level, which is the last row here, so
	// start at the top to make a "down" observable.
	m.effortDlg.cursor = 0
	before := m.effortDlg.cursor
	m.handleChatKey("down", false)
	if m.effortDlg == nil {
		t.Fatal("routing closed the dialog")
	}
	if m.effortDlg.cursor == before && len(m.effortDlg.choices) > 1 {
		t.Fatal("the key never reached the dialog")
	}
}

// A level that is not a level at all is a different failure from one the model
// does not offer, and both surfaces must say so rather than silently ignoring
// the argument.
func TestEffortSlashRejectsAnUnparseableLevel(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.handleSlash("/effort turbo")
	if got := m.session.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("garbage changed the effort to %q", got)
	}
	if m.effortDlg != nil {
		t.Fatal("a rejected argument must not open the picker")
	}

	res := effortCatalogConfig()
	sess := newEffortSessionForPlain(t)
	out := &strings.Builder{}
	if _, _, err := handleSlash("/effort turbo", sess, res, false, &Terminal{out: out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "turbo") {
		t.Fatalf("plain surface must name the rejected value, got %q", out.String())
	}
	if got := sess.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("garbage changed the effort to %q", got)
	}
}

// effortNoDefaultTUI drives a model that declares efforts with NO configured
// default: it ships sending no reasoning field, which is a state the picker
// must be able to return to.
func effortNoDefaultTUI(t *testing.T) *tuiModel {
	t.Helper()
	res := effortCatalogConfig()
	res.Model = effortThinker
	res.ModelProfiles = []config.ModelSpec{
		{
			Name: effortThinker, ContextWindowTokens: 200000,
			ReasoningEfforts: []reasoning.Level{reasoning.Off, reasoning.Low, reasoning.High},
			ReasoningDialect: reasoning.DialectThinkingEffort,
		},
		{Name: effortPlain, ContextWindowTokens: 200000},
	}
	m := newTUIModel(chat.NewSession(res, welcomeStubCompleter{}), res, true)
	m.mode = modeChat
	return m
}

// Picking a level must not be one-way. The picker offers the shipped state as
// its own row, marks it as the default, and distinguishes it from off.
func TestIntegrationEffortDialogReturnsToAModelsUnsetDefault(t *testing.T) {
	m := effortNoDefaultTUI(t)
	m.width, m.height = 90, 24
	m.handleSlash("/effort")
	if m.effortDlg == nil {
		t.Fatal("/effort did not open a dialog")
	}
	view, _ := m.effortDlg.ViewAt(90, 24)
	text := stripANSI(view)
	if !strings.Contains(text, "unset") {
		t.Fatalf("picker offers no route back to the shipped state:\n%s", text)
	}
	unsetLine := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "unset") {
			unsetLine = line
		}
	}
	if !strings.Contains(unsetLine, "default") {
		t.Fatalf("the shipped state must be labelled the default:\n%s", text)
	}
	if !strings.Contains(unsetLine, "no reasoning field") {
		t.Fatalf("unset must be distinguishable from off:\n%s", text)
	}

	m.effortDlg.cursor = len(m.effortDlg.choices) - 1
	m.handleEffortDialogKey("enter")
	if got := m.session.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("effort = %q after choosing the last row", got)
	}
	m.handleSlash("/effort")
	m.effortDlg.cursor = 0
	m.handleEffortDialogKey("enter")
	if m.effortDlg != nil {
		t.Fatal("an accepted selection must close the dialog")
	}
	if got := m.session.ReasoningEffort(); got.Active() {
		t.Fatalf("effort = %q, want the picker to have cleared it", got)
	}
}

// A model WITH a configured default needs no extra row: its default is already
// one of the declared levels and is labelled there.
func TestEffortDialogAddsNoUnsetRowWhenTheModelHasADefault(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.handleSlash("/effort")
	if got := len(m.effortDlg.choices); got != 3 {
		t.Fatalf("rows = %d, want the 3 declared levels", got)
	}
	if !strings.Contains(stripANSI(mustEffortView(t, m)), "high (default)") {
		t.Fatalf("configured default is not labelled:\n%s", stripANSI(mustEffortView(t, m)))
	}
}

func mustEffortView(t *testing.T, m *tuiModel) string {
	t.Helper()
	view, _ := m.effortDlg.ViewAt(90, 24)
	return view
}

// The typed argument is the plain surface's only route back, so it must accept
// the same word the picker row shows - and then report the state that word
// actually produced, which on a model with a configured default is that
// default rather than silence.
func TestEffortSlashArgumentClearsTheOverride(t *testing.T) {
	res := effortCatalogConfig()
	sess := newEffortSessionForPlain(t)
	if err := sess.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	out := &strings.Builder{}
	if _, _, err := handleSlash("/effort unset", sess, res, false, &Terminal{out: out}); err != nil {
		t.Fatal(err)
	}
	if got := sess.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("effort = %q, want the configured default back", got)
	}
	assertEffortLineMatchesTheWire(t, out.String(), sess.ReasoningSetting())
}

// assertEffortLineMatchesTheWire holds every /effort confirmation to the dial
// the next request will carry: the printed sentence names the effective level
// and never claims silence while a level is still being sent.
func assertEffortLineMatchesTheWire(t *testing.T, printed string, setting reasoning.Setting) {
	t.Helper()
	printed = stripANSI(printed)
	if setting.Active() {
		if !strings.Contains(printed, string(setting.Level)) {
			t.Fatalf("printed %q, but %q goes on the wire", printed, setting.Level)
		}
		if strings.Contains(printed, "no reasoning field") {
			t.Fatalf("printed %q while %q goes on the wire", printed, setting.Level)
		}
		return
	}
	if !strings.Contains(printed, "no reasoning field") {
		t.Fatalf("printed %q, but nothing goes on the wire", printed)
	}
}

// Both surfaces confirm the outcome, not the argument. Clearing an override on
// a model that has a configured default leaves that default on the wire.
func TestEffortUnsetOnADefaultedModelReportsTheDefaultInForce(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.handleSlash("/effort low")
	m.handleSlash("/effort unset")
	last := ""
	if len(m.blocks) > 0 {
		last = m.blocks[len(m.blocks)-1].Text
	}
	assertEffortLineMatchesTheWire(t, last, m.session.ReasoningSetting())

	m.handleSlash("/effort")
	m.effortDlg.cursor = 0
	m.handleEffortDialogKey("enter")
	if len(m.blocks) > 0 {
		last = m.blocks[len(m.blocks)-1].Text
	}
	assertEffortLineMatchesTheWire(t, last, m.session.ReasoningSetting())
}

// A model that offers no route back must not be told to take one: the picker
// hides the unset row for a defaulted model, so the summary must hide the word
// too, and both must show it for a model that ships unset.
func TestEffortSummaryOffersUnsetOnlyWhereThePickerDoes(t *testing.T) {
	defaulted := effortCatalogConfig()
	sess := chat.NewSession(defaulted, welcomeStubCompleter{})
	summary := formatEffortSummary(sess.CurrentModel(), sess.ReasoningChoices(), sess.ReasoningEffort(), sess.ReasoningDefault())
	if strings.Contains(summary, effortUnsetWord) {
		t.Fatalf("summary offers a no-op route back: %q", summary)
	}

	m := effortNoDefaultTUI(t)
	shipped := m.session
	summary = formatEffortSummary(shipped.CurrentModel(), shipped.ReasoningChoices(), shipped.ReasoningEffort(), shipped.ReasoningDefault())
	if !strings.Contains(summary, ", or "+effortUnsetWord) {
		t.Fatalf("summary hides the only route back: %q", summary)
	}
}

// The direct-argument path must refuse in the same words as the picker, rather
// than leaking the session's own wording for a state the UI already knows.
func TestEffortSlashArgumentRefusesWhileBusy(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.waiting = true
	m.handleSlash("/effort low")
	if got := m.session.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("a busy surface changed the effort to %q", got)
	}
	last := ""
	if len(m.blocks) > 0 {
		last = stripANSI(m.blocks[len(m.blocks)-1].Text)
	}
	if !strings.Contains(last, "finish current work first") {
		t.Fatalf("the busy refusal must match the picker's wording, got %q", last)
	}
}

// /status is where an operator checks what they are running, so it must name
// the dial on both surfaces - it is the only place the state is readable
// without changing it.
func TestStatusReportsTheReasoningDial(t *testing.T) {
	m := effortTUI(t, effortThinker)
	if err := m.session.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	text := stripANSI(strings.Join(m.newStatusDialog().lines, "\n"))
	setting := m.session.ReasoningSetting()
	if !strings.Contains(text, string(setting.Level)) || !strings.Contains(text, string(setting.Dialect)) {
		t.Fatalf("status omits the dial (%+v):\n%s", setting, text)
	}

	res := effortCatalogConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	out := &strings.Builder{}
	if _, _, err := handleSlash("/status", sess, res, false, &Terminal{out: out}); err != nil {
		t.Fatal(err)
	}
	plain := stripANSI(out.String())
	if !strings.Contains(plain, string(sess.ReasoningSetting().Level)) {
		t.Fatalf("plain /status omits the dial:\n%s", plain)
	}
}

// A model with no reasoning surface must read as such rather than as an empty
// value the operator has to interpret.
func TestStatusSaysSomethingForAModelThatOffersNothing(t *testing.T) {
	m := effortTUI(t, effortPlain)
	if value := statusEffortValue(t, m); !strings.Contains(value, "no reasoning efforts") {
		t.Fatalf("effort row = %q, want it to say the model declares none", value)
	}
}

// /status must distinguish the three states a dial can be in, because "no
// reasoning field is sent" and "this model has none to send" look identical on
// the wire and mean different things to the operator.
func TestFormatEffortStatusNamesEachState(t *testing.T) {
	cases := map[string]struct {
		setting reasoning.Setting
		offers  bool
		want    []string
	}{
		"model offers nothing": {
			reasoning.Setting{}, false,
			[]string{"none", "declares no reasoning"},
		},
		// A dialect is a wire shape, not a declared set. Reading one as evidence
		// that the model offers levels is the lie /status exists to prevent.
		"a dialect alone is not an offer": {
			reasoning.Setting{Dialect: reasoning.DialectOpenAI}, false,
			[]string{"none", "declares no reasoning"},
		},
		"offers but dialled off": {
			reasoning.Setting{Dialect: reasoning.DialectThinking}, true,
			[]string{effortUnsetWord, "no reasoning field"},
		},
		"active with a dialect": {
			reasoning.Setting{Level: reasoning.High, Dialect: reasoning.DialectThinkingEffort}, true,
			[]string{"high", "thinking_effort"},
		},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			got := formatEffortStatus(tc.setting, tc.offers)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("status = %q, want it to mention %q", got, want)
				}
			}
			if tc.setting.Dialect == "" && strings.Contains(got, "·") && tc.setting.Level.Active() {
				t.Fatalf("status named a dialect that does not exist: %q", got)
			}
		})
	}
}
