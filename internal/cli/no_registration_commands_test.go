package cli

import (
	"slices"
	"strings"
	"testing"
)

// registrationCommands are the account-creation verbs this CLI deliberately
// does not have. The product decision is that accounts are created in the web
// app only; the CLI authenticates an account that already exists.
//
// This is a guard, not a preference. `mivia register` and `mivia verify`
// existed against the retired go-mivia contract and were removed when the CLI
// moved to /v1. The API is now growing POST /v1/auth/register and
// POST /v1/auth/activate again (apps/api Phase A), so the endpoints these
// commands would call are about to exist. That makes re-adding them a one-line
// change somebody could make without noticing it reverses a decision.
//
// loginRequestError in login.go tells a user with no account to sign up at the
// web app, and states that the CLI has no way to create one. That sentence is
// only true while this test passes.
var registrationCommands = []string{
	"register",
	"verify",
	"activate",
	"signup",
	"sign-up",
}

// TestRegistrationCommandsAreNotDispatched pins the behavior a user sees: the
// verb is not routed anywhere, it falls through to the unknown-command error.
func TestRegistrationCommandsAreNotDispatched(t *testing.T) {
	for _, name := range registrationCommands {
		t.Run(name, func(t *testing.T) {
			err := Execute([]string{name})
			if err == nil {
				t.Fatalf("Execute([%q]) returned nil; the CLI must not expose account creation - see the comment on registrationCommands", name)
			}
			if !strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("Execute([%q]) error = %v; want the unknown-command error, which means %q now dispatches somewhere", name, err, name)
			}
		})
	}
}

// TestRegistrationCommandsAreNotAdvertised covers the other half: a command
// absent from the dispatch switch but present in completion would still tell
// users it exists, and completionCommands is maintained by hand.
func TestRegistrationCommandsAreNotAdvertised(t *testing.T) {
	for _, name := range registrationCommands {
		if slices.Contains(completionCommands, name) {
			t.Errorf("completionCommands advertises %q; the CLI does not create accounts - see the comment on registrationCommands", name)
		}
	}
}
