package uiadapter

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestTurnTapSendRacesTurnEndClose pins the send/close exclusion on a
// turn's event channel for the NORMAL completion path.
//
// The per-turn tap checks `closed.Load()` and then sends on `events`
// (newTurnHandler's forward). The turn goroutine ends the turn via
// emitTurnEndIfWinner, which CASes the same flag and calls
// `close(h.events)` - and, critically, runTurnGoroutine only calls
// cancelTurn() AFTER that, so the per-turn context is still LIVE while
// the channel is already closed. The tap's
// `select { case events <- e: case <-turnCtx.Done(): }` therefore has
// exactly one ready arm - the send - and sending on a closed channel
// panics on the agent-loop goroutine, taking the whole TUI down as a
// streaming turn completes.
//
// The tap detaches only via h.restore(), which runTurnGoroutine defers
// to the very end, so the tap is still attached across this window.
//
// The unbuffered channel and absent reader are deliberate: they widen a
// window that a buffered channel with a live reader makes rarer, never
// impossible.
func TestTurnTapSendRacesTurnEndClose(t *testing.T) {
	for i := 0; i < 50; i++ {
		events := make(chan uievent.Event)
		closed := &atomic.Bool{}
		// The per-turn context stays LIVE for the whole window, exactly as
		// it is when emitTurnEndIfWinner closes the channel.
		turnCtx, cancelTurn := context.WithCancel(context.Background())

		var seq uint64
		// One stream, shared by the tap and the handle - exactly as Send
		// wires them.
		stream := newTurnStream(events, turnCtx.Done(), cancelTurn)
		handler := newTurnHandler(
			stream, closed, &atomic.Pointer[string]{}, &seq,
			turnCtx, TranslateOptions{}, nil,
		)
		h := newTurnHandle(events, closed, cancelTurn, func() {}, stream)
		c := &Conversation{}

		var wg sync.WaitGroup
		wg.Add(1)

		// The agent loop: emits synchronously into the still-attached tap.
		// With no reader this parks in forward's select, which is exactly
		// the state a real tap is in whenever the UI is momentarily behind.
		go func() {
			defer wg.Done()
			handler(agent.Event{Kind: agent.EventAssistant, Content: "hi", Detail: "delta"})
		}()

		// Wait until the tap is genuinely parked on the send before ending
		// the turn. The value must NOT be received: draining it unparks the
		// sender and closes the very window under test.
		waitForBlockedSender()

		// The turn goroutine: ends the turn, closing the channel under the
		// parked sender.
		c.emitTurnEndIfWinner(h, closed, &seq, "turn-1", nil)

		wg.Wait()
		cancelTurn()
	}
}

// waitForBlockedSender yields long enough for the emitting goroutine to
// reach its send and park there. It deliberately does not touch the
// channel: receiving the value would complete the send and dissolve the
// send-in-flight state this test needs at the moment of close.
func waitForBlockedSender() {
	for i := 0; i < 100; i++ {
		runtime.Gosched()
	}
	time.Sleep(2 * time.Millisecond)
}
