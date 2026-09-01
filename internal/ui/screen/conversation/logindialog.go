package conversation

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/field"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// loginFieldWidth is the fixed inner width given to the email and
// password fields. The dialog frame itself is sized by dialogSize /
// render.Dialog, the same as every other modal in this package.
const loginFieldWidth = 40

// loginDialog is the state of an open /login modal: an email field and a
// masked password field. It never imports internal/miviaauth - the hint
// line below names no API host, keeping the ui-isolation boundary
// (docs/design/ui-isolation.md) intact: only internal/uiadapter is
// allowed to know about miviaauth.
type loginDialog struct {
	Theme theme.Theme
	Tier  theme.Tier

	email    field.Model
	password field.Model
	focus    int // 0 = email, 1 = password
}

func newLoginDialog(t theme.Theme, tier theme.Tier) loginDialog {
	email := field.New(t, tier, "email", field.KindText, loginFieldWidth)
	password := field.New(t, tier, "password", field.KindText, loginFieldWidth)
	password.SetEchoMasked()
	return loginDialog{Theme: t, Tier: tier, email: email, password: password}
}

// openLogin opens the login dialog, optionally prefilling the email
// field from a /login <email> argument. The email field is always
// focused first, prefilled or not: the user can edit it immediately, or
// Tab/Enter straight past it to the password.
func (s Screen) openLogin(prefill string) (Screen, tea.Cmd) {
	dlg := newLoginDialog(s.Theme, s.Tier)
	if prefill != "" {
		dlg.email.SetValue(prefill)
	}
	cmd := dlg.email.Focus()
	s.login = &dlg
	return s, tea.Batch(cmd, tea.ClearScreen)
}

// focusPassword moves focus from the email field to the password field.
func (d loginDialog) focusPassword() (loginDialog, tea.Cmd) {
	d.email.Blur()
	cmd := d.password.Focus()
	d.focus = 1
	return d, cmd
}

// Update routes one key press to the focused field, except Tab and
// Enter on the email field, which move focus to the password field
// instead of typing a literal tab or newline. Enter on the password
// field is NOT handled here: submitting needs the runner and the
// screen's own quit-arm state, so the caller (handleLoginKey) intercepts
// it before this is reached.
func (d loginDialog) Update(msg tea.KeyPressMsg) (loginDialog, tea.Cmd) {
	if msg.String() == "tab" && d.focus == 0 {
		return d.focusPassword()
	}
	if msg.String() == "enter" && d.focus == 0 {
		return d.focusPassword()
	}
	if d.focus == 0 {
		next, cmd := d.email.Update(msg)
		d.email = next
		return d, cmd
	}
	next, cmd := d.password.Update(msg)
	d.password = next
	return d, cmd
}

// View draws the two fields under a static hint. The hint names no API
// host on purpose (see the package doc comment on loginDialog).
func (d loginDialog) View() string {
	note := render.Role(d.Theme, d.Tier, theme.RoleFGSubtle).Render("enter your mivia account details")
	return note + "\n\n" + d.email.View() + "\n" + d.password.View()
}

// renderLoginDialog draws the login dialog as a centered dialog, the
// same primitive every other modal in this package uses.
func renderLoginDialog(t theme.Theme, tier theme.Tier, width, height int, d loginDialog) string {
	return render.Dialog(t, tier, width, height, "sign in", d.View(),
		"[enter] next / submit  [tab] switch field  [esc] cancel")
}

// loginResultMsg carries a completed login attempt's outcome back into
// the screen's Update loop. Keeping the network call in a tea.Cmd (see
// submitLogin) rather than calling CompleteLogin synchronously is what
// keeps the whole UI responsive while the request is in flight.
type loginResultMsg struct {
	outcome ports.CommandOutcome
}

// submitLogin closes the dialog immediately and starts the asynchronous
// CompleteLogin call. The password is copied into a byte slice here
// and zeroed inside the returned Cmd once the call returns - it is
// never written into the composer, the transcript, or any notice; see
// CommandOutcome.LoginPrompt's doc comment and CompleteLogin's
// contract.
func (s Screen) submitLogin() (Screen, tea.Cmd) {
	if s.login == nil {
		return s, nil
	}
	email := s.login.email.Value()
	pwBytes := []byte(s.login.password.Value())
	s.login = nil
	if s.runner == nil {
		clear(pwBytes)
		return s.withError("no command runner configured for /login"), tea.ClearScreen
	}
	runner := s.runner
	cmd := func() tea.Msg {
		out := runner.CompleteLogin(context.Background(), email, pwBytes)
		clear(pwBytes)
		return loginResultMsg{outcome: out}
	}
	return s, tea.Batch(cmd, tea.ClearScreen)
}
