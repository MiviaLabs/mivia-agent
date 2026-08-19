package conversation

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/themepicker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

// fakeRunner is a hand-written ports.CommandRunner: Run and SelectModel
// each return a fixed outcome and record every call, so a test can
// assert both the dispatch (name/args reaching the runner) and the
// screen's reaction to the outcome.
type fakeRunner struct {
	outcome       ports.CommandOutcome
	selectOutcome ports.CommandOutcome
	calls         []string
	selectCalls   []string
}

func (f *fakeRunner) Run(_ context.Context, name, args string) ports.CommandOutcome {
	f.calls = append(f.calls, name+"|"+args)
	return f.outcome
}

func (f *fakeRunner) SelectModel(_ context.Context, name string) ports.CommandOutcome {
	f.selectCalls = append(f.selectCalls, name)
	return f.selectOutcome
}

// sendLine types text into the composer and presses Enter once. Every
// test in this file constructs its Screen with no SetCommands call, so
// the completion menu never opens and one Enter reaches composerAction
// directly - unlike the cmd/mivia-ui teatest suite, which drives the
// real candidate list and therefore needs two Enters (rule 5.6).
func sendLine(t *testing.T, s Screen, line string) (Screen, tea.Cmd) {
	t.Helper()
	s = typeText(t, s, line)
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return next.(Screen), cmd
}

func lastErrorDetail(t *testing.T, s Screen) string {
	t.Helper()
	blocks := s.transcript.Blocks()
	if len(blocks) == 0 {
		t.Fatal("expected at least one transcript block")
	}
	return blocks[len(blocks)-1].Header.Detail
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		line     string
		wantName string
		wantArgs string
	}{
		{"/model", "model", ""},
		{"/model gpt-5", "model", "gpt-5"},
		{"/model   gpt-5  ", "model", "gpt-5"},
		{"/", "", ""},
	}
	for _, c := range cases {
		name, args := splitCommand(c.line)
		if name != c.wantName || args != c.wantArgs {
			t.Errorf("splitCommand(%q) = (%q, %q), want (%q, %q)", c.line, name, args, c.wantName, c.wantArgs)
		}
	}
}

func TestIsSlashCommand(t *testing.T) {
	if !isSlashCommand("/model") {
		t.Error("expected /model to be a slash command")
	}
	if isSlashCommand("hello") {
		t.Error("expected plain text not to be a slash command")
	}
	if isSlashCommand("") {
		t.Error("expected empty text not to be a slash command")
	}
}

func TestRunSlashCommandNilRunnerShowsErrorNotSend(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s, cmd := sendLine(t, s, "/bogus")
	if cmd != nil {
		t.Errorf("got cmd %v, want nil (no turn started)", cmd)
	}
	if got := lastErrorDetail(t, s); got != "no command runner configured for /bogus" {
		t.Errorf("got error detail %q, want the no-runner message", got)
	}
	if s.composer.Value() != "" {
		t.Errorf("got composer %q, want cleared", s.composer.Value())
	}
}

func TestRunSlashCommandErrOutcomeShowsError(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{Err: "unknown command /bogus"}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)

	s, _ = sendLine(t, s, "/bogus extra args")
	if got := lastErrorDetail(t, s); got != "unknown command /bogus" {
		t.Errorf("got error detail %q, want the runner's Err text", got)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "bogus|extra args" {
		t.Errorf("got calls %v, want one call to Run(\"bogus\", \"extra args\")", runner.calls)
	}
}

func TestRunSlashCommandQuit(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{Quit: true}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)

	_, cmd := sendLine(t, s, "/quit")
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd for Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("got %T, want tea.QuitMsg", cmd())
	}
}

func TestRunSlashCommandOpenTheme(t *testing.T) {
	themes := []theme.Theme{loadTheme(t)}
	runner := &fakeRunner{outcome: ports.CommandOutcome{OpenTheme: true}}
	s := newScreen(t, replay.New(nil, 0), nil, themes)
	s.SetCommandRunner(runner)

	_, cmd := sendLine(t, s, "/theme")
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd for OpenTheme")
	}
	msg, ok := cmd().(app.PushScreenMsg)
	if !ok {
		t.Fatalf("got %T, want app.PushScreenMsg", cmd())
	}
	if _, ok := msg.Screen.(themepicker.Screen); !ok {
		t.Errorf("got screen %T, want themepicker.Screen", msg.Screen)
	}
}

func TestRunSlashCommandOpenThemeNoThemesIsNoOp(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{OpenTheme: true}}
	s := newScreen(t, replay.New(nil, 0), nil, nil) // no themes configured
	s.SetCommandRunner(runner)

	_, cmd := sendLine(t, s, "/theme")
	if cmd != nil {
		t.Errorf("got cmd %v, want nil when no themes are configured", cmd)
	}
}

func TestRunSlashCommandOpenHelp(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{OpenHelp: true}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)

	s, _ = sendLine(t, s, "/help")
	if s.overlay == "" {
		t.Error("expected /help to set the overlay")
	}
}

// TestRunSlashCommandOpenHelpAppendsMouseHint pins the branch that adds
// the terminal's mouse-override hint (rule 6.5) to the /help overlay
// when one was recorded via SetMouseOverrideHint.
func TestRunSlashCommandOpenHelpAppendsMouseHint(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{OpenHelp: true}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s.SetMouseOverrideHint("shift")

	s, _ = sendLine(t, s, "/help")
	if !strings.Contains(s.overlay, "hold shift") {
		t.Errorf("got overlay %q, want it to mention the mouse-override hint", s.overlay)
	}
}

// TestRunSlashCommandEmptyOutcomeIsANoOp pins the fallback: an outcome
// with every field at its zero value (a CommandRunner that forgot to
// set one) changes nothing rather than panicking or guessing.
func TestRunSlashCommandEmptyOutcomeIsANoOp(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	before := len(s.transcript.Blocks())

	s, cmd := sendLine(t, s, "/noop")
	if cmd != nil {
		t.Errorf("got cmd %v, want nil", cmd)
	}
	if got := len(s.transcript.Blocks()); got != before {
		t.Errorf("got %d blocks, want %d unchanged", got, before)
	}
}

func TestRunSlashCommandClearTranscript(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{ClearTranscript: true}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s.Notice("leftover content")
	if len(s.transcript.Blocks()) == 0 {
		t.Fatal("expected the notice to have been pushed before /clear")
	}

	s, _ = sendLine(t, s, "/clear")
	if got := len(s.transcript.Blocks()); got != 0 {
		t.Errorf("got %d blocks after /clear, want 0", got)
	}
}

func TestRunSlashCommandNotice(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{Notice: "context usage: 10%"}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)

	s, _ = sendLine(t, s, "/context")
	if got := lastErrorDetail(t, s); got != "context usage: 10%" {
		t.Errorf("got notice detail %q, want the runner's Notice text", got)
	}
}

func TestRunSlashCommandModelChoicesOpensPicker(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{ModelChoices: []string{"a", "b"}}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)

	s, cmd := sendLine(t, s, "/model")
	if cmd != nil {
		t.Errorf("got cmd %v, want nil (the picker opens in place)", cmd)
	}
	if s.modelPicker == nil {
		t.Fatal("expected /model to open the model picker")
	}
}

func TestHandleModelPickerKeySelectAppliesOutcome(t *testing.T) {
	runner := &fakeRunner{
		outcome:       ports.CommandOutcome{ModelChoices: []string{"fast", "deep"}},
		selectOutcome: ports.CommandOutcome{Notice: "model set to fast"},
	}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/model")
	if s.modelPicker == nil {
		t.Fatal("expected the model picker to be open")
	}

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // accept the highlighted ("fast") choice
	s = next.(Screen)

	if s.modelPicker != nil {
		t.Error("expected the picker to close after a selection")
	}
	if len(runner.selectCalls) != 1 || runner.selectCalls[0] != "fast" {
		t.Errorf("got selectCalls %v, want one call with \"fast\"", runner.selectCalls)
	}
	if got := lastErrorDetail(t, s); got != "model set to fast" {
		t.Errorf("got notice detail %q, want the SelectModel outcome's Notice", got)
	}
}

func TestHandleModelPickerKeyCancelClosesWithoutNotice(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{ModelChoices: []string{"fast", "deep"}}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/model")
	before := len(s.transcript.Blocks())

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	s = next.(Screen)

	if s.modelPicker != nil {
		t.Error("expected esc to close the picker")
	}
	if got := len(s.transcript.Blocks()); got != before {
		t.Errorf("got %d blocks, want %d (esc adds no notice)", got, before)
	}
	if len(runner.selectCalls) != 0 {
		t.Errorf("got selectCalls %v, want none after a cancel", runner.selectCalls)
	}
}

// TestHandleModelPickerKeyFilterKeystrokeIsAbsorbed pins the "picker
// stays open, nothing else happens" branch: a plain character types
// into the picker's own filter (picker.Model's job), producing no Cmd
// and no transcript change.
func TestHandleModelPickerKeyFilterKeystrokeIsAbsorbed(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{ModelChoices: []string{"fast", "deep"}}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/model")
	before := len(s.transcript.Blocks())

	next, cmd := s.Update(tea.KeyPressMsg{Text: "f", Code: 'f'})
	s = next.(Screen)

	if cmd != nil {
		t.Errorf("got cmd %v, want nil (the picker absorbs the keystroke)", cmd)
	}
	if s.modelPicker == nil {
		t.Error("expected the picker to stay open")
	}
	if got := len(s.transcript.Blocks()); got != before {
		t.Errorf("got %d blocks, want %d unchanged", got, before)
	}
}

func TestHandleModelPickerKeySelectWithNilRunnerShowsError(t *testing.T) {
	// A defensive path: the picker was opened by a runner that was
	// since cleared. Constructed directly (package-internal field
	// access) since SetCommandRunner(nil) after opening the picker is
	// the only way to reach it through the public API.
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	pm := picker.New(s.Theme, s.Tier, []string{"only"})
	s.modelPicker = &pm
	s.runner = nil

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	if got := lastErrorDetail(t, s); got != "no command runner configured for /model" {
		t.Errorf("got error detail %q, want the no-runner message", got)
	}
}

func TestViewRendersModelPickerWhenActive(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{ModelChoices: []string{"fast", "deep"}}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s.width, s.height = 40, 10
	s, _ = sendLine(t, s, "/model")

	view := s.View()
	if !containsAll(view, "select a model", "fast", "deep") {
		t.Errorf("got view %q, want it to render the model picker", view)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
