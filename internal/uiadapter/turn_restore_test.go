package uiadapter

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestStaleRestoreDoesNotClobberTheLiveTap pins ownership of the session's
// OnAgentEvent slot.
//
// Every turn does `previous := SwapOnAgentEvent(handler)` and later
// `SwapOnAgentEvent(previous)`. That pair is only correct while the turn
// still OWNS the slot. Two shipped paths break that:
//
//   - runTurnGoroutine registers `defer h.restore()` BEFORE
//     `defer c.turnMu.Unlock()`, and defers run LIFO, so the mutex that
//     gates Send is released FIRST. The next queued turn can acquire it and
//     install its own tap before the finished turn's restore runs.
//   - turnHandle.Cancel is documented safe to call after a turn has ended,
//     and it calls h.restore() unconditionally.
//
// Either way a finished turn writes its own stale `previous` (typically
// nil) over the LIVE turn's tap. The live turn then streams nothing: its
// agent events reach no sink, so the UI shows a spinner and a turn that
// never produces output.
//
// This drives the Cancel path because it is deterministic; the goroutine
// path is the same single write through the same unguarded swap.
func TestStaleRestoreDoesNotClobberTheLiveTap(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, nil)
	c := NewConversation(sess)

	// Turn 1 installs its tap and finishes. previous1 is nil.
	events1 := make(chan uievent.Event, turnBufferSize)
	closed1 := &atomic.Bool{}
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	stream1 := newTurnStream(events1, ctx1.Done(), cancel1)
	handler1 := newTurnHandler(stream1, closed1, &atomic.Pointer[string]{}, new(uint64), ctx1, TranslateOptions{}, nil)
	previous1, token1 := sess.SwapOnAgentEventToken(handler1)
	h1 := newTurnHandle(events1, closed1, cancel1, func() {
		sess.RestoreOnAgentEvent(token1, previous1)
	}, stream1)
	_ = c

	// Turn 2 begins and installs ITS tap - it now owns the slot.
	var live atomic.Int64
	handler2 := func(agent.Event) { live.Add(1) }
	sess.SwapOnAgentEvent(handler2)

	// The finished turn 1's handle is cancelled (the UI may cancel a handle
	// it still holds after the turn ended - Cancel's own doc says so).
	h1.Cancel()

	// Turn 2's tap must still be installed. A nil slot here IS the defect:
	// turn 1's restore wrote its own stale previous (nil) over it.
	tap := sess.OnAgentEvent
	if tap == nil {
		t.Fatal("a finished turn's restore nulled the live turn's tap; the live turn streams nothing to the UI")
	}
	tap(agent.Event{Kind: agent.EventAssistant, Content: "hi", Detail: "delta"})
	if live.Load() == 0 {
		t.Error("a finished turn's restore clobbered the live turn's tap; the live turn now streams nothing to the UI")
	}
}
