package chat

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/hooksession"
)

// fireRootTurnEndHook fires the Stop hook for one completed root turn and
// surfaces its output through the same OnAgentEvent callback Pre/PostToolUse
// hook runs use (see internal/agent/hook_events.go's emitHookRuns), so a Stop
// hook is visible the same way a tool hook is wherever EventHook is rendered.
//
// Call this wrapped around done() in sendPlain/sendAgent's own defer, not
// inside beginPlainTurn/beginAgentTurn's done closure: sendPlain/sendAgent's
// defer runs on every return path once begin*Turn has already succeeded, so a
// turn that began fires Stop on every outcome - success, error, or a canceled
// ctx - while a turn that never began (session switching or loading, an error
// from begin*Turn itself) fires nothing. That matches Stop's own "the
// assistant is done" semantics: a turn that never started has nothing to
// report as done.
//
// Firing is synchronous on the turn's own critical path: -p, --plain, line
// mode, and the TUI all funnel through sendUserWithTurn (internal/chat's sole
// funnel, see AGENTS.md-linked docs/development/lifecycle-hooks.md), so a
// slow Stop handler delays every one of them by up to EventStop's configured
// timeout (5s, internal/hooks/config.go). That is chosen deliberately over an
// async fire-and-forget: Stop's contract already promises the hook runs under
// the turn's own ctx so a cancellation is recorded rather than silently
// skipped (hooksession.RunStopForTurn), and a detached goroutine cannot honor
// that without its own synchronization back to this call.
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
