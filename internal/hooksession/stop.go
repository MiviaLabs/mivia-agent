package hooksession

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
)

// RunStopForTurn fires Stop hooks for a completed ROOT turn and returns their
// output as an attributed continuation prompt.
//
// Stop is pure observation: it has no denial channel at all, so a Stop hook can
// log a turn's cost and can never affect whether the turn ended.
//
// This is internal/chat's single call site (see stop_hook.go there), reached
// from every surface (-p, --plain, line mode, TUI) because all four funnel
// through chat.Session.sendUserWithTurn, and fired once per root turn - a turn
// that began (sendPlain/sendAgent past beginPlainTurn/beginAgentTurn) fires
// Stop on every outcome (success, error, or a canceled ctx); a turn that
// never began (session switching or loading) fires nothing. PreToolUse and
// PostToolUse are unaffected: they run through the dispatcher's Policy, not
// through this seam.
//
// workspaceRoot is not a parameter: it is the same directory Install resolved
// this session's hook argv[0] paths against (Session.workspaceRoot), so a
// caller that already knows its sessionID/turnID does not need to also thread
// the workspace root through every turn-completion path.
//
// The context is the turn's own, deliberately not a detached one. A canceled
// turn therefore does not run its Stop hook, and the run is RECORDED rather
// than skipped silently - detaching would make Ctrl-C wait out the hook's
// timeout before the cancelled footer appeared.
func RunStopForTurn(ctx context.Context, sessionID, turnID string) string {
	session := Current()
	groups := session.RunnableGroups()
	if len(groups) == 0 {
		return ""
	}
	outcome := hooks.Runner{WorkspaceRoot: session.workspaceRoot}.Run(ctx, groups, hooks.Payload{
		Event:     hooks.EventStop,
		SessionID: sessionID,
		TurnID:    turnID,
	})
	session.NoteRunWarnings(outcome.Warnings)
	return outcome.Context
}
