package conversation

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// typeIntoLogin sends each rune of text as a key press. Used only while
// s.login is open, so every key reaches handleLoginKey (checked first in
// handleModalKey) rather than the composer.
func typeIntoLogin(t *testing.T, s Screen, text string) Screen {
	t.Helper()
	for _, r := range text {
		next, _ := s.Update(keyMsg(string(r)))
		s = next.(Screen)
	}
	return s
}

func TestRunSlashCommandLoginOpensDialogNoPrefill(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{LoginPrompt: true}}
	s := newScreen(t, nil, nil, nil)
	s.SetCommandRunner(runner)

	s, cmd := sendLine(t, s, "/login")
	if s.login == nil {
		t.Fatal("expected /login to open the login dialog")
	}
	if got := s.login.email.Value(); got != "" {
		t.Errorf("got prefilled email %q, want empty", got)
	}
	if !hasClearScreen(cmd) {
		t.Error("expected /login to clear the screen so nothing bleeds through under the dialog")
	}
}

func TestRunSlashCommandLoginPrefillsEmail(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{LoginPrompt: true, LoginEmail: "user@example.com"}}
	s := newScreen(t, nil, nil, nil)
	s.SetCommandRunner(runner)

	s, _ = sendLine(t, s, "/login user@example.com")
	if s.login == nil {
		t.Fatal("expected /login to open the login dialog")
	}
	if got := s.login.email.Value(); got != "user@example.com" {
		t.Errorf("got prefilled email %q, want user@example.com", got)
	}
}

func TestLoginDialogEscCancelsWithoutNotice(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{LoginPrompt: true}}
	s := newScreen(t, nil, nil, nil)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/login")
	before := len(s.transcript.Blocks())

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	s = next.(Screen)

	if s.login != nil {
		t.Error("expected esc to close the login dialog")
	}
	if got := len(s.transcript.Blocks()); got != before {
		t.Errorf("got %d blocks, want %d (esc adds no notice)", got, before)
	}
	if len(runner.loginCalls) != 0 {
		t.Errorf("esc submitted a login attempt: %v", runner.loginCalls)
	}
}

// TestLoginDialogCtrlCIsTheEmergencyExit: the same rule as the queue
// overlay - ctrl+c closes the dialog and arms the quit state instead of
// being swallowed by the modal.
func TestLoginDialogCtrlCIsTheEmergencyExit(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{LoginPrompt: true}}
	s := newScreen(t, nil, nil, nil)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/login")

	next, _ := s.Update(ctrl('c'))
	s = next.(Screen)
	if s.login != nil {
		t.Error("ctrl+c left the login dialog open")
	}
	if !s.quitArmed {
		t.Error("expected quit to be armed on first ctrl+c")
	}
}

// TestLoginDialogTabMovesFocusToPassword pins the field order: Tab (or
// Enter) on the email field moves focus to the password field without
// typing a literal tab into either.
func TestLoginDialogTabMovesFocusToPassword(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{LoginPrompt: true}}
	s := newScreen(t, nil, nil, nil)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/login")

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	s = next.(Screen)
	if s.login.focus != 1 {
		t.Fatalf("got focus %d, want 1 (password) after Tab", s.login.focus)
	}
	if s.login.email.Value() != "" {
		t.Errorf("Tab typed into the email field: %q", s.login.email.Value())
	}
}

// TestLoginDialogEnterOnPasswordSubmits drives the whole happy path: type
// an email, move to the password field, type a password, press Enter, and
// confirm the runner receives both values and the resulting Notice lands
// in the transcript.
func TestLoginDialogEnterOnPasswordSubmits(t *testing.T) {
	runner := &fakeRunner{
		outcome:      ports.CommandOutcome{LoginPrompt: true},
		loginOutcome: ports.CommandOutcome{Notice: "Logged in as user@example.com."},
	}
	s := newScreen(t, nil, nil, nil)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/login")

	s = typeIntoLogin(t, s, "user@example.com")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	s = next.(Screen)
	s = typeIntoLogin(t, s, "hunter2")

	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.login != nil {
		t.Error("expected the dialog to close immediately on submit")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd for the async CompleteLogin call")
	}

	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("got %T, want tea.BatchMsg (the login Cmd plus ClearScreen)", msg)
	}
	var result tea.Msg
	for _, c := range batch {
		if c == nil {
			continue
		}
		if lr, ok := c().(loginResultMsg); ok {
			result = lr
		}
	}
	if result == nil {
		t.Fatal("expected the batch to carry a loginResultMsg")
	}

	next, _ = s.Update(result)
	s = next.(Screen)

	if len(runner.loginCalls) != 1 || runner.loginCalls[0] != "user@example.com|7" {
		t.Errorf("got loginCalls %v, want [\"user@example.com|7\"]", runner.loginCalls)
	}
	if got := lastErrorDetail(t, s); got != "Logged in as user@example.com." {
		t.Errorf("got notice detail %q, want the login confirmation", got)
	}
}

// TestLoginDialogErrorOutcomeShowsErrorBlock covers CompleteLogin
// reporting a rejected login: the dialog is already closed by the time
// the result arrives, and the error lands as an ordinary transcript
// error block.
func TestLoginDialogErrorOutcomeShowsErrorBlock(t *testing.T) {
	runner := &fakeRunner{
		outcome:      ports.CommandOutcome{LoginPrompt: true},
		loginOutcome: ports.CommandOutcome{Err: "the email or password was not accepted."},
	}
	s := newScreen(t, nil, nil, nil)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/login")
	s = typeIntoLogin(t, s, "user@example.com")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	s = next.(Screen)
	s = typeIntoLogin(t, s, "wrongpw")

	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	msg := cmd()
	batch := msg.(tea.BatchMsg)
	var result tea.Msg
	for _, c := range batch {
		if c == nil {
			continue
		}
		if lr, ok := c().(loginResultMsg); ok {
			result = lr
		}
	}
	next, _ = s.Update(result)
	s = next.(Screen)

	if got := lastErrorDetail(t, s); got != "the email or password was not accepted." {
		t.Errorf("got error detail %q, want the runner's Err text", got)
	}
}

// TestLoginDialogViewNeverShowsRawPassword pins the mask contract at the
// screen's own rendered View, not just the field package's unit test: the
// typed password must never appear in plain text while the dialog is open.
func TestLoginDialogViewNeverShowsRawPassword(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{LoginPrompt: true}}
	s := sized(t, 0)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/login")

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	s = next.(Screen)
	s = typeIntoLogin(t, s, "supersecret")

	view := ansi.Strip(s.View())
	if strings.Contains(view, "supersecret") {
		t.Errorf("rendered view leaked the raw password:\n%s", view)
	}
	if !strings.Contains(view, "•") {
		t.Errorf("rendered view missing the password mask:\n%s", view)
	}
}

// TestLoginDialogNilRunnerShowsError covers submitting with no runner
// configured: submitLogin must report a transcript error instead of
// panicking on a nil CompleteLogin call.
func TestLoginDialogNilRunnerShowsError(t *testing.T) {
	s := newScreen(t, nil, nil, nil)
	s.login = &loginDialog{}
	dlg := newLoginDialog(s.Theme, s.Tier)
	s.login = &dlg
	s.login.focus = 1

	next, cmd := s.submitLogin()
	sc := next
	if sc.login != nil {
		t.Error("expected submitLogin to close the dialog even with no runner")
	}
	if got := lastErrorDetail(t, sc); got != "no command runner configured for /login" {
		t.Errorf("got error detail %q, want the no-runner message", got)
	}
	if cmd == nil {
		t.Error("expected a non-nil Cmd (ClearScreen) even on the no-runner path")
	}
}
