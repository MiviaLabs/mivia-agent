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
// Scope, stated rather than assumed: KindTurnEnd's only publish site is the
// root TUI turn goroutine (internal/cli/tui_events.go), so Stop fires once per
// user-visible turn and never per subagent turn - a per-subagent Stop would run
// N times and its "the assistant is done" semantics would be false every time
// but the last. The same fact bounds the feature: the classic --plain REPL and
// the -p one-shot never publish KindTurnEnd, so Stop does not fire there. That
// is a seam gap, not a design choice, and it is recorded here rather than
// papered over. PreToolUse and PostToolUse do fire on those surfaces; Stop is
// the one event a -p run silently does without.
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
