package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/charmbracelet/x/ansi"
)

const (
	effortThinker = "glm-5.2"
	effortPlain   = "glm-4.6"
)

func effortCatalogConfig() *config.Resolved {
	return &config.Resolved{
		ProviderName: "zai",
		Model:        effortThinker,
		Models:       []string{effortThinker, effortPlain},
		ModelProfiles: []config.ModelSpec{
			{
				Name: effortThinker, ContextWindowTokens: 200000,
				ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.Medium, reasoning.High},
				Reasoning:        reasoning.High,
				ReasoningDialect: reasoning.DialectThinkingEffort,
			},
			{Name: effortPlain, ContextWindowTokens: 200000},
		},
	}
}

func effortTUI(t *testing.T, model string) *tuiModel {
	t.Helper()
	res := effortCatalogConfig()
	res.Model = model
	m := newTUIModel(chat.NewSession(res, welcomeStubCompleter{}), res, true)
	m.mode = modeChat
	return m
}

func dialogText(t *testing.T, view string) string {
	t.Helper()
	return stripANSI(view)
}

// loadReasoningConfig builds a catalog the way an operator does, from TOML.
// Reasoning resolution starts at the file: a hand-built Resolved can hold a
// shape config.Load would reject, and - the reason this helper takes the model
// lines verbatim - it can only ever hold a dialect that was written down, never
// the far more ordinary entry that leaves the dialect to its provider.
func loadReasoningConfig(t *testing.T, defaultModel, modelLines string) *config.Resolved {
	t.Helper()
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("ZAI_API_KEY=picker-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mivia.toml")
	body := "env_file = \"" + filepath.ToSlash(env) + "\"\n\n" + `[provider]
name = "zai"

[providers.zai]
models = [
` + modelLines + `
]
default_model = "` + defaultModel + `"

[chat]
max_tokens = 8192
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// loadEffortCatalog builds a real catalog: ModelCatalog() is only populated by
// config.Load, so a hand-built Resolved renders an empty picker.
func loadEffortCatalog(t *testing.T) *config.Resolved {
	t.Helper()
	return loadReasoningConfig(t, effortThinker,
		`  { name = "`+effortThinker+`", context_window_tokens = 200000, reasoning_efforts = ["low", "medium", "high"], reasoning = "high", reasoning_dialect = "thinking_effort" },
  { name = "`+effortPlain+`", context_window_tokens = 200000 },`)
}

// loadedEffortTUI drives the surfaces against a config.Load catalog.
func loadedEffortTUI(t *testing.T, res *config.Resolved) *tuiModel {
	t.Helper()
	m := newTUIModel(chat.NewSession(res, welcomeStubCompleter{}), res, true)
	m.mode = modeChat
	return m
}

// statusEffortValue reads the effort row out of the /status dialog.
func statusEffortValue(t *testing.T, m *tuiModel) string {
	t.Helper()
	text := stripANSI(strings.Join(m.newStatusDialog().lines, "\n"))
	value := ""
	for _, candidate := range strings.Split(text, "\n") {
		if after, found := strings.CutPrefix(strings.TrimSpace(candidate), "effort"); found {
			value = strings.TrimSpace(after)
		}
	}
	if value == "" {
		t.Fatalf("status has no effort row, or the row is blank:\n%s", text)
	}
	return value
}

const (
	// effortDefaulted lists efforts and leaves reasoning_dialect out, which zai
	// resolves to its vetted default. Its set is off plus one graded level
	// because the thinking dialect cannot carry depth, and config.Load refuses a
	// set that dialect would flatten.
	effortDefaulted = "glm-4.7"
	// effortDialectOnly is the mirror shape: a declared wire dialect and no
	// levels to send through it.
	effortDialectOnly = "glm-4.8"
)

func loadDefaultedDialectConfig(t *testing.T) *config.Resolved {
	t.Helper()
	return loadReasoningConfig(t, effortDefaulted,
		`  { name = "`+effortDefaulted+`", context_window_tokens = 200000, reasoning_efforts = ["off", "high"], reasoning = "high" },`)
}

// The most ordinary reasoning entry an operator writes names levels and leaves
// the wire shape to the provider. /status reading that as "declares no
// reasoning efforts" sends them hunting for a key that is already correct, and
// contradicts the /effort picker standing beside it.
func TestIntegrationStatusReadsAModelThatLeavesTheDialectToTheProvider(t *testing.T) {
	m := loadedEffortTUI(t, loadDefaultedDialectConfig(t))
	value := statusEffortValue(t, m)
	if strings.Contains(value, "no reasoning efforts") {
		t.Fatalf("effort row = %q for a model declaring off and high", value)
	}
	for _, want := range []string{string(reasoning.High), string(reasoning.DialectThinking)} {
		if !strings.Contains(value, want) {
			t.Fatalf("effort row = %q, want it to name %q", value, want)
		}
	}
}

// The picker and /status read the same configuration, so a level the picker
// offers must not be a level /status says does not exist.
func TestIntegrationDefaultedDialectModelOffersItsDeclaredEfforts(t *testing.T) {
	m := loadedEffortTUI(t, loadDefaultedDialectConfig(t))
	m.handleSlash("/effort off")
	if got := m.session.ReasoningEffort(); got != reasoning.Off {
		t.Fatalf("effort = %q after /effort off, want off", got)
	}
	if value := statusEffortValue(t, m); !strings.Contains(value, string(reasoning.DialectThinking)) {
		t.Fatalf("effort row = %q, want the resolved dialect", value)
	}
}

// A declared dialect is a statement about the wire shape, not about whether
// there is anything to send through it. /status must agree with the refusal
// /effort gives on the same model.
func TestIntegrationStatusReadsADialectWithNoDeclaredEfforts(t *testing.T) {
	res := loadReasoningConfig(t, effortDialectOnly,
		`  { name = "`+effortDialectOnly+`", context_window_tokens = 200000, reasoning_dialect = "openai" },`)
	m := loadedEffortTUI(t, res)
	if value := statusEffortValue(t, m); !strings.Contains(value, "no reasoning efforts") {
		t.Fatalf("effort row = %q, want it to say the model declares none", value)
	}
	if err := m.session.SetReasoningEffort(reasoning.High); err == nil {
		t.Fatal("/effort accepted a level on a model that declares none")
	}
}

// The /model picker must make the dial visible at selection time - that is the
// whole reason reasoning is per model rather than a session-global setting.
func TestIntegrationModelDialogShowsTheDefaultEffort(t *testing.T) {
	res := loadEffortCatalog(t)
	d := newModelDialog(res.ModelCatalog(), chat.Selection{ProviderName: "zai", Model: effortThinker}, string(reasoning.High), false)
	view, _ := d.ViewAt(90, 24)
	text := dialogText(t, view)
	if !strings.Contains(text, effortThinker) || !strings.Contains(text, "effort: high") {
		t.Fatalf("picker did not annotate the configured model:\n%s", text)
	}
	// A catalog that uses no reasoning must not grow a column of "none".
	plainLine := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, effortPlain) {
			plainLine = line
		}
	}
	if plainLine == "" {
		t.Fatalf("model %q missing from picker:\n%s", effortPlain, text)
	}
	if strings.Contains(plainLine, "effort") {
		t.Fatalf("a model offering nothing was annotated: %q", plainLine)
	}
}

func TestIntegrationEffortDialogListsDeclaredEffortsInConfigOrder(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.handleSlash("/effort")
	if m.effortDlg == nil {
		t.Fatal("/effort did not open a dialog")
	}
	view, _ := m.effortDlg.ViewAt(90, 24)
	text := dialogText(t, view)
	lowAt, mediumAt, highAt := strings.Index(text, "low"), strings.Index(text, "medium"), strings.Index(text, "high")
	if lowAt < 0 || mediumAt < 0 || highAt < 0 {
		t.Fatalf("dialog missing a declared effort:\n%s", text)
	}
	if !(lowAt < mediumAt && mediumAt < highAt) {
		t.Fatalf("efforts are not in config order:\n%s", text)
	}
	// A level the model never declared must not appear.
	if strings.Contains(text, "xhigh") || strings.Contains(text, "minimal") {
		t.Fatalf("dialog offered an undeclared level:\n%s", text)
	}
	if !strings.Contains(text, "default") {
		t.Fatalf("dialog must label the model default:\n%s", text)
	}
}

// The empty state is the requirement: a model with nothing declared must SAY
// so, naming itself, rather than showing an empty list the user cannot read.
func TestIntegrationEffortDialogExplainsAModelThatOffersNothing(t *testing.T) {
	m := effortTUI(t, effortPlain)
	m.handleSlash("/effort")
	if m.effortDlg == nil {
		t.Fatal("/effort must open a dialog even when nothing is configured")
	}
	view, _ := m.effortDlg.ViewAt(90, 24)
	text := dialogText(t, view)
	if !strings.Contains(text, effortPlain) {
		t.Fatalf("empty state must name the model:\n%s", text)
	}
	if !strings.Contains(strings.ToLower(text), "no reasoning effort") {
		t.Fatalf("empty state must explain itself:\n%s", text)
	}
	// Enter on an empty list must be inert, not a panic or a silent change.
	m.handleEffortDialogKey("enter")
	if got := m.session.ReasoningEffort(); got.Active() {
		t.Fatalf("enter on an empty picker set %q", got)
	}
}

func TestIntegrationEffortDialogSelectionChangesTheNextRequest(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.handleSlash("/effort")
	if m.effortDlg == nil {
		t.Fatal("/effort did not open a dialog")
	}
	// Cursor starts on the effective level (high, the default); move to low.
	m.handleEffortDialogKey("home")
	m.handleEffortDialogKey("enter")
	if m.effortDlg != nil {
		t.Fatal("selecting an effort must close the dialog")
	}
	if got := m.session.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("effort = %q, want low", got)
	}
	if got := m.session.CurrentBinding().Profile.Reasoning; got != reasoning.Low {
		t.Fatalf("the captured binding carried %q, want low", got)
	}
}

func TestIntegrationEffortDialogMarksTheEffectiveLevel(t *testing.T) {
	m := effortTUI(t, effortThinker)
	if err := m.session.SetReasoningEffort(reasoning.Medium); err != nil {
		t.Fatal(err)
	}
	m.handleSlash("/effort")
	view, _ := m.effortDlg.ViewAt(90, 24)
	for _, line := range strings.Split(dialogText(t, view), "\n") {
		if !strings.Contains(line, "medium") {
			continue
		}
		if !strings.Contains(line, "●") {
			t.Fatalf("the effective level is not marked: %q", line)
		}
		return
	}
	t.Fatalf("medium row missing:\n%s", dialogText(t, view))
}

func TestIntegrationEffortSlashWithAnArgumentSetsDirectly(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.handleSlash("/effort low")
	if m.effortDlg != nil {
		t.Fatal("an explicit argument must not open the picker")
	}
	if got := m.session.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("effort = %q, want low", got)
	}
}

func TestIntegrationEffortSlashRejectsAnUndeclaredLevel(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.handleSlash("/effort xhigh")
	if got := m.session.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("a refused level changed the effort to %q", got)
	}
}

// INV-TUI-29: an open modal owns the screen. A dialog missing from the
// modal-open predicate leaks mouse and paste events to the transcript.
func TestIntegrationEffortDialogIsATrueModal(t *testing.T) {
	m := effortTUI(t, effortThinker)
	m.handleSlash("/effort")
	if !m.modalOpen() {
		t.Fatal("the effort dialog must register as an open modal")
	}
	m.closeModal()
	if m.effortDlg != nil {
		t.Fatal("close-all left the effort dialog open")
	}
}

func TestIntegrationEffortDialogStaysWithinTinyCanvases(t *testing.T) {
	for _, model := range []string{effortThinker, effortPlain} {
		m := effortTUI(t, model)
		m.handleSlash("/effort")
		for _, size := range []struct{ width, height int }{{1, 1}, {2, 8}, {24, 2}, {90, 24}} {
			view, layout := m.effortDlg.ViewAt(size.width, size.height)
			if layout.Rect.X < 0 || layout.Rect.Y < 0 ||
				layout.Rect.X+layout.Rect.W > size.width || layout.Rect.Y+layout.Rect.H > size.height {
				t.Fatalf("%s %dx%d out-of-bounds: %+v", model, size.width, size.height, layout)
			}
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > layout.Rect.W {
					t.Fatalf("%s %dx%d line width=%d Rect=%d: %q",
						model, size.width, size.height, ansi.StringWidth(line), layout.Rect.W, stripANSI(line))
				}
			}
		}
	}
}

// /effort is registered for both surfaces, so the classic REPL must answer it
// too - with the same information the picker would have shown.
func TestIntegrationEffortSlashOnThePlainSurface(t *testing.T) {
	res := effortCatalogConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	out := &strings.Builder{}
	term := &Terminal{out: out}

	if _, _, err := handleSlash("/effort", sess, res, false, term); err != nil {
		t.Fatal(err)
	}
	summary := out.String()
	for _, want := range []string{"high", effortThinker, "low, medium, high"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary must mention %q, got %q", want, summary)
		}
	}

	out.Reset()
	if _, _, err := handleSlash("/effort low", sess, res, false, term); err != nil {
		t.Fatal(err)
	}
	if got := sess.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("effort = %q, want low", got)
	}

	out.Reset()
	if _, _, err := handleSlash("/effort xhigh", sess, res, false, term); err != nil {
		t.Fatal(err)
	}
	if got := sess.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("a refused level changed the effort to %q", got)
	}
	if !strings.Contains(out.String(), "xhigh") {
		t.Fatalf("refusal must name the level, got %q", out.String())
	}
}

// A model that offers nothing must say so on the plain surface as well.
func TestIntegrationEffortSlashPlainEmptyState(t *testing.T) {
	res := effortCatalogConfig()
	res.Model = effortPlain
	sess := chat.NewSession(res, welcomeStubCompleter{})
	out := &strings.Builder{}
	if _, _, err := handleSlash("/effort", sess, res, false, &Terminal{out: out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no reasoning effort configured for "+effortPlain) {
		t.Fatalf("empty state = %q", out.String())
	}
}
