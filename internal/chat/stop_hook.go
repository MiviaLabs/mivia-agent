package chat

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/hooksession"
)

// fireRootTurnEndHook executes the Stop hook for one completed root turn. It
// sends output to OnAgentEvent like Pre/PostToolUse hooks (see emitHookRuns in
// internal/agent/hook_events.go) so EventHook renders Stop hooks like tool hooks.
//
// Call this around done() in sendPlain and sendAgent defers, not in the
// begin*Turn done closures. sendPlain and sendAgent defers run on every return path
// after begin*Turn succeeds. The hook fires on success, error, or canceled ctx.
// Turns that never start (switch, load, or begin*Turn failure) fire nothing.
// This preserves Stop semantics ("the assistant is done").
//
// Execution is synchronous on the turn path. All surfaces funnel through
// sendUserWithTurn, so slow handlers delay turns by up to EventStop timeout (5s,
// internal/hooks/config.go). Synchronous execution ensures the hook runs under
// turn ctx so cancellations record properly (hooksession.RunStopForTurn).
// Detached goroutines cannot honor this contract without external synchronization.
//
// This uses a direct internal/hooksession import without an injected seam.
// sessionID and turnID are parameters, making calls multi-session safe.
// hooksession is a leaf package with no import cycle risk.
func (s *Session) fireRootTurnEndHook(ctx context.Context, sessionID string, myTurn uint64) {
	if !hooksession.Configured() {
		return
	}
	output := hooksession.RunStopForTurn(ctx, sessionID, fmt.Sprintf("turn:%d", myTurn))
	if output == "" {
		return
	}
	s.mu.RLock()
	onEvent := s.OnAgentEvent
	s.mu.RUnlock()
	if onEvent == nil {
		return
	}
	onEvent(agent.Event{Kind: agent.EventHook, Name: "Stop", Output: output})
}
