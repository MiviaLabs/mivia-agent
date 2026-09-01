package uiadapter

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// webAppURL is where accounts are created. There is no sign-up command in
// the TUI: registration happens in the web app only.
const webAppURL = "https://mivia.app"

// handleLogin parses /login's arguments and opens the login dialog. It
// never authenticates itself: the dialog collects the password (never
// typed into the composer or the transcript) and submits it through
// CompleteLogin. Re-login is always allowed, even with a session already
// active - nothing destructive happens before the dialog is submitted.
func (r *CommandRunner) handleLogin(args string) ports.CommandOutcome {
	fields := strings.Fields(args)
	switch len(fields) {
	case 0:
		return ports.CommandOutcome{LoginPrompt: true}
	case 1:
		return ports.CommandOutcome{LoginPrompt: true, LoginEmail: fields[0]}
	default:
		return ports.CommandOutcome{Err: "usage: /login [email] - the password is asked separately"}
	}
}

// CompleteLogin authenticates email and password against the configured
// mivia API and persists the resulting session. password is zeroed before
// this returns, whether or not the login succeeded, so the login dialog's
// buffer never outlives the request it was collected for.
func (r *CommandRunner) CompleteLogin(ctx context.Context, email string, password []byte) ports.CommandOutcome {
	defer clear(password)

	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") {
		return ports.CommandOutcome{Err: "enter a valid email address"}
	}
	if len(password) == 0 {
		return ports.CommandOutcome{Err: "enter your password"}
	}

	svcFn := r.loginService
	if svcFn == nil {
		svcFn = miviaauth.DefaultService
	}
	svc, err := svcFn()
	if err != nil {
		return ports.CommandOutcome{Err: "login failed: " + err.Error()}
	}

	if err := svc.Login(ctx, email, password); err != nil {
		return ports.CommandOutcome{Err: loginErrorText(err)}
	}
	// Closes the login-after-session-start sync gap: any session already
	// pooled before this login (chat started while logged out) had no
	// chat-sync session, because attachSyncLocked's active-sync check was
	// false at construction time. A successful login re-checks it for
	// every pooled session. r.pool is non-nil for every CommandRunner this
	// package constructs (NewCommandRunner always calls NewSessionPool);
	// the nil guard just makes this safe for a CommandRunner built by
	// hand with no pool at all.
	if r.pool != nil {
		r.pool.ReattachSyncAfterLogin()
	}
	return ports.CommandOutcome{Notice: "Logged in as " + email + "."}
}

// loginErrorText turns a failed login into a message with a next step. It
// is a local copy of internal/cli's own loginRequestError: internal/uiadapter
// must not import internal/cli, and the message text stays identical on
// purpose so the TUI and the classic CLI report the same thing.
//
// The server answers an unknown account and a wrong password with the same
// 401, deliberately, so it leaks nothing about which addresses have
// accounts. The message therefore covers both without claiming to know
// which one happened.
func loginErrorText(err error) string {
	var statusErr *miviaauth.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusUnauthorized:
			return "the email or password was not accepted. Check the password, or sign up at " + webAppURL + " if you do not have an account yet"
		case http.StatusBadRequest:
			if statusErr.Detail != "" {
				return "the server rejected the request: " + statusErr.Detail
			}
		case http.StatusTooManyRequests:
			return "rate limited, wait a few minutes and try again"
		}
	}
	return "login failed: " + err.Error()
}
