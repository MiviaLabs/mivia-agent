// This file (headless.go) implements D5's headless turn-drive helper: a
// session can be driven with no TUI attached, but only with a mandatory
// drain to channel close. See docs/design/automations.md's D5 for the
// full rationale (the per-turn channel is buffered at 32 and
// turnStream.Send blocks when full - a consumer that stops reading
// before close leaves the agent-loop goroutine parked, wedging every
// later Send on that conversation).
package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// sendTurnHeadless sends prompt as one turn on an ALREADY-SPAWNED
// conversation and drains its Events() channel to CLOSE - ranging to
// close, never breaking on KindTurnEnd, because Events() closes exactly
// once regardless of which of Cancel/emitTurnEnd wins (turn_stream.go,
// per ports.TurnHandle's own doc comment) and breaking early leaves any
// later tap send on this conversation parked forever.
//
// ctx is ALWAYS given a deadline (timeout) before Send is called: when
// nothing drains a full turn channel, turnStream.Send's <-s.done arm is
// the only thing that ever unparks the blocked sender, and cancelTurn()
// itself only runs downstream of that same blocked send - so an
// undeadlined ctx has no escape hatch at all if draining ever stops.
//
// This is chunk 6's split of the former single-shot runTurnHeadless: the
// executor dispatches SEVERAL steps in order on ONE spawned conversation
// (D2's "mixed action... in ONE session"), so the spawn and the
// per-step send/drain must be separate calls. runTurnHeadless (below)
// remains a thin wrapper - spawn once, then call this - so every
// existing caller (the wedge tests in headless_test.go/
// headless_fakes_test.go) keeps its call shape unchanged.
func sendTurnHeadless(parent context.Context, conv ports.Conversation, prompt string, timeout time.Duration) ([]uievent.Event, error) {
	if conv == nil {
		return nil, fmt.Errorf("automation: no conversation to send a headless turn on")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("automation: headless turn requires a positive timeout")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	h, err := conv.Send(ctx, intent.Send{Text: prompt})
	if err != nil {
		return nil, fmt.Errorf("automation: send turn: %w", err)
	}
	var events []uievent.Event
	var turnErr error
	var lastNotice string
	for ev := range h.Events() { // drain to CLOSE - see doc comment above
		events = append(events, ev)
		switch ev.Kind {
		case uievent.KindError:
			if body, ok := ev.Body.(uievent.ErrorBody); ok {
				turnErr = fmt.Errorf("automation: turn failed: %s", body.Text)
			} else {
				turnErr = fmt.Errorf("automation: turn failed")
			}
		case uievent.KindNotice:
			// emitTurnEndIfWinner (internal/uiadapter/conversation.go)
			// sends the turn's error text as a KindNotice immediately
			// before the terminal KindTurnEnd{Reason:"error"} - captured
			// here so the TurnEnd branch below can report it verbatim
			// instead of a bare "turn ended in error".
			if body, ok := ev.Body.(uievent.NoticeBody); ok {
				lastNotice = body.Text
			}
		case uievent.KindTurnEnd:
			if body, ok := ev.Body.(uievent.TurnEndBody); ok && body.Reason == "error" && turnErr == nil {
				if lastNotice != "" {
					turnErr = fmt.Errorf("automation: turn failed: %s", lastNotice)
				} else {
					turnErr = fmt.Errorf("automation: turn ended in error")
				}
			}
		}
	}
	return events, turnErr
}

// runTurnHeadless spawns a fresh conversation in worktreeDir, sends
// prompt as one turn, and drains it via sendTurnHeadless. Each call uses
// its own fresh conversation (via spawn.CreateFreshInDir) and never
// reuses one across calls, so a wedge on one run cannot cross into
// another. Kept as a thin wrapper over sendTurnHeadless purely so
// chunk 2's wedge tests (headless_test.go, headless_fakes_test.go) keep
// their exact call shape; the executor (executor.go) calls
// sendTurnHeadless directly against its own single spawned conversation
// instead, per D2's "several steps, one session".
func runTurnHeadless(parent context.Context, spawn SessionSpawner, bindFn func(*chat.Session) (string, error), worktreeDir, prompt string, timeout time.Duration) ([]uievent.Event, error) {
	if spawn == nil {
		return nil, fmt.Errorf("automation: no session spawner configured")
	}
	conv, err := spawn.CreateFreshInDir(bindFn, worktreeDir)
	if err != nil {
		return nil, fmt.Errorf("automation: spawn session: %w", err)
	}
	return sendTurnHeadless(parent, conv, prompt, timeout)
}
