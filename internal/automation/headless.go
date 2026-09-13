// This file (headless.go) implements the headless turn-drive helper: a
// session can be driven with no TUI attached, but only with a mandatory
// drain to channel close. See "Headless Session Safety" in
// docs/design/automations.md for the
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
// conversation and drains it exactly as sendIntentHeadless does - a thin
// wrapper building the intent.Send this package's callers need in the
// common case (no PersistedText override). See sendIntentHeadless's own
// doc comment for the full drain/cancellation contract.
func sendTurnHeadless(parent context.Context, conv ports.Conversation, prompt string, timeout time.Duration) ([]uievent.Event, error) {
	return sendIntentHeadless(parent, conv, intent.Send{Text: prompt}, timeout)
}

// sendIntentHeadless sends in as one turn on an ALREADY-SPAWNED
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
// This is the per-step half of runTurnHeadless: the executor
// dispatches SEVERAL steps in order on ONE spawned conversation
// ("Step Kinds"), so the spawn and the
// per-step send/drain must be separate calls. runTurnHeadless (below)
// remains a thin wrapper - spawn once, then call this - so every
// existing caller (the wedge tests in headless_test.go/
// headless_fakes_test.go) keeps its call shape unchanged.
//
// A caller with a full intent.Send in hand (StepSkill's own dispatch,
// skillstep.go, needs a distinct PersistedText from Text) calls this
// directly instead of going through sendTurnHeadless, which only ever
// builds a bare intent.Send{Text: prompt}.
func sendIntentHeadless(parent context.Context, conv ports.Conversation, in intent.Send, timeout time.Duration) ([]uievent.Event, error) {
	if conv == nil {
		return nil, fmt.Errorf("automation: no conversation to send a headless turn on")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("automation: headless turn requires a positive timeout")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	h, err := conv.Send(ctx, in)
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
			turnErr = turnEndErr(ev, lastNotice, turnErr)
		}
	}
	// A cancelled parent is the run's own cancellation: report it
	// even when the turn ended without an error event.
	if err := parent.Err(); err != nil {
		return events, fmt.Errorf("automation: turn cancelled: %w", err)
	}
	return events, turnErr
}

// turnEndErr maps a terminal KindTurnEnd event to an error. Reason
// "error" reports lastNotice when present; reason "cancelled" is an
// error too, never a success. An earlier turnErr is kept.
func turnEndErr(ev uievent.Event, lastNotice string, turnErr error) error {
	body, ok := ev.Body.(uievent.TurnEndBody)
	if !ok || turnErr != nil {
		return turnErr
	}
	switch body.Reason {
	case "error":
		if lastNotice != "" {
			return fmt.Errorf("automation: turn failed: %s", lastNotice)
		}
		return fmt.Errorf("automation: turn ended in error")
	case "cancelled":
		return fmt.Errorf("automation: turn cancelled")
	}
	return nil
}

// runTurnHeadless spawns a fresh conversation in worktreeDir, sends
// prompt as one turn, and drains it via sendTurnHeadless. Each call uses
// its own fresh conversation (via spawn.CreateFreshInDir) and never
// reuses one across calls, so a wedge on one run cannot cross into
// another. Kept as a thin wrapper over sendTurnHeadless purely so
// the wedge tests (headless_test.go, headless_fakes_test.go) keep
// their exact call shape; the executor (executor.go) calls
// sendTurnHeadless directly against its own single spawned conversation
// instead: several steps, one session.
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
