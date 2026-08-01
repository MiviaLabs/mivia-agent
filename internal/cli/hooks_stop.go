package cli

import (
	"context"
)

// pushStopHookOutput fires this turn's Stop hooks and renders their output as
// an attributed transcript row.
//
// It runs BEFORE bridge.Finish: Finish is a one-way latch that hands the turn
// to the drain, and a push after it would race a superseded turn. It runs on
// the turn goroutine, not the UI goroutine, so a slow hook delays only the
// goroutine's exit - and only until its 5s default, since a canceled turn
// short-circuits.
func (m *tuiModel) pushStopHookOutput(ctx context.Context, bridge *streamBridge, turnID string) {
	if m == nil || bridge == nil {
		return
	}
	text := runStopHookEvent(ctx, m.hookWorkspaceRoot(), m.sessionIDForHooks(), turnID)
	if text == "" {
		return
	}
	bridge.PushCompletedBanner("stop hook", text)
}

func (m *tuiModel) hookWorkspaceRoot() string {
	if m.agentState == nil {
		return ""
	}
	return m.agentState.WorkspaceRoot
}

func (m *tuiModel) sessionIDForHooks() string {
	if m.session == nil {
		return ""
	}
	return m.session.SessionID
}
