package hooksession

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
)

// RunStopEvent fires Stop hooks for a completed ROOT turn and returns their
// output as an attributed continuation prompt.
//
// Stop is pure observation: it has no denial channel at all, so a Stop hook can
// log a turn's cost and can never affect whether the turn ended.
//
// Scope, stated rather than assumed: this function has NO production caller
// on any surface today (TUI included) - see
// docs/development/lifecycle-hooks.md's "Limitation" note. Wiring it needs a
// real per-turn call site with a genuine turn identifier and a design that
// reaches every session a process can run, not just one; that is future
// work, tracked separately. PreToolUse and PostToolUse are unaffected: they
// run through the dispatcher's Policy, not through this seam.
//
// The context is the turn's own, deliberately not a detached one. A canceled
// turn therefore does not run its Stop hook, and the run is RECORDED rather
// than skipped silently - detaching would make Ctrl-C wait out the hook's
// timeout before the cancelled footer appeared.
func RunStopEvent(ctx context.Context, workspaceRoot, sessionID, turnID string) string {
	session := Current()
	groups := session.RunnableGroups()
	if len(groups) == 0 {
		return ""
	}
	outcome := hooks.Runner{WorkspaceRoot: workspaceRoot}.Run(ctx, groups, hooks.Payload{
		Event:     hooks.EventStop,
		SessionID: sessionID,
		TurnID:    turnID,
	})
	session.NoteRunWarnings(outcome.Warnings)
	return outcome.Context
}
