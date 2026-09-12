// conversation_live_internal_test.go covers the fan-out behaviors that
// need package-private access: overflow marks a viewer stale once and
// then drops its events instead of blocking the producer. It also
// white-box tests beginTurnProgress/endTurnProgress's generation-token
// bookkeeping directly (package uiadapter, not uiadapter_test), which
// lets these tests call the unexported methods synchronously instead of
// going through Send/goroutine timing - the deterministic complement to
// conversation_background_test.go's black-box, Send-driven coverage of
// the same fix.
package uiadapter

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestBroadcast_OverflowMarksStaleOnceThenDrops fills a stopped viewer's
// buffer, asserts Stale closes exactly once, and that further broadcasts
// neither block nor reach the viewer after the mark.
func TestBroadcast_OverflowMarksStaleOnceThenDrops(t *testing.T) {
	// The registry under test touches no session state, so a sessionless
	// conversation is enough.
	conv := NewConversation(nil)
	events, sub := conv.SubscribeLive()
	defer sub.Close()

	ev := uievent.Event{Kind: uievent.KindNotice}
	// Fill the buffer: no reader, so every send after the cap takes the
	// default arm - the producer must not block.
	for i := 0; i < liveViewerBufferSize+8; i++ {
		conv.broadcast(ev)
	}

	select {
	case <-sub.Stale():
	default:
		t.Fatal("Stale() not closed after overflowing a stopped viewer's buffer")
	}

	// Draining the buffered events must not re-mark or un-drop: the
	// viewer is stale for good and only a resubscribe recovers.
	for drained := 0; drained < liveViewerBufferSize; {
		select {
		case <-events:
			drained++
		case <-time.After(time.Second):
			t.Fatalf("buffered %d events before stalling, want %d", drained, liveViewerBufferSize)
		}
	}
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("received an event after the stale mark; post-mark events must be dropped")
		}
		t.Fatal("events channel was closed; the publisher must never close it")
	default:
	}

	// More broadcasts after the mark: no block and no double stale close.
	// A second close would panic inside broadcast, so reaching here - and
	// under -race too - is the assertion; Stale being closed is not
	// re-checkable by receive (a closed channel always receives).
	for i := 0; i < 16; i++ {
		conv.broadcast(ev)
	}
}

// TestBeginEndTurnProgress_LateReleaseFromOlderGenerationIsNoOp is the
// deterministic, synchronous proof of the slice 2 fix: calling
// beginTurnProgress twice (simulating turn N then turn N+1 starting
// before turn N's endTurnProgress has run - exactly the window
// runTurnGoroutine's defer ordering opens; see progressGen's doc
// comment on Conversation) and then calling endTurnProgress with the
// FIRST call's stale token must not touch the second acquisition at
// all: progressHandler and progressRelease stay set to turn N+1's
// values, and the registrar's release closure is not invoked.
func TestBeginEndTurnProgress_LateReleaseFromOlderGenerationIsNoOp(t *testing.T) {
	var acquireCount, releaseCount int
	prevRegistrar := SubagentProgressRegistrar
	defer func() { SubagentProgressRegistrar = prevRegistrar }()

	c := NewConversation(nil) // foreground by construction; see NewConversation
	handlerN := func(agent.Event) {}
	handlerNPlus1 := func(agent.Event) {}

	SubagentProgressRegistrar = func(fn func(agent.Event)) func() {
		acquireCount++
		return func() { releaseCount++ }
	}

	genN := c.beginTurnProgress(handlerN)
	if acquireCount != 1 {
		t.Fatalf("beginTurnProgress(N) acquired %d time(s), want 1", acquireCount)
	}

	// Turn N+1 starts BEFORE turn N's endTurnProgress has run - the
	// exact overlap the defer-ordering bug produces.
	genNPlus1 := c.beginTurnProgress(handlerNPlus1)
	if genNPlus1 == genN {
		t.Fatalf("beginTurnProgress returned the same generation twice: %d", genN)
	}
	// beginTurnProgress(N+1) itself released N's acquisition (the
	// "second turn on this conversation registers its own sink" path)
	// and re-acquired for N+1 - that is intended, ordinary turn
	// replacement, not the bug. acquireCount/releaseCount reflect it.
	if acquireCount != 2 {
		t.Fatalf("beginTurnProgress(N+1) acquired %d time(s) total, want 2", acquireCount)
	}
	if releaseCount != 1 {
		t.Fatalf("beginTurnProgress(N+1)'s internal replace-release fired %d time(s), want 1", releaseCount)
	}
	if c.progressHandler == nil {
		t.Fatal("progressHandler is nil after beginTurnProgress(N+1); want handlerNPlus1 installed")
	}
	if c.progressRelease == nil {
		t.Fatal("progressRelease is nil after beginTurnProgress(N+1); want N+1's acquisition held")
	}

	// Turn N's goroutine finally runs its deferred endTurnProgress,
	// carrying genN - now stale, since progressGen has moved to
	// genNPlus1's value.
	c.endTurnProgress(genN)

	if releaseCount != 1 {
		t.Fatalf("stale endTurnProgress(genN) changed releaseCount to %d, want unchanged at 1", releaseCount)
	}
	if c.progressHandler == nil {
		t.Fatal("stale endTurnProgress(genN) cleared progressHandler; turn N+1's sink must survive")
	}
	if c.progressRelease == nil {
		t.Fatal("stale endTurnProgress(genN) released the registrar; turn N+1's acquisition must survive")
	}

	// The CURRENT generation's own release still works normally.
	c.endTurnProgress(genNPlus1)
	if releaseCount != 2 {
		t.Fatalf("endTurnProgress(genNPlus1) produced releaseCount=%d, want 2", releaseCount)
	}
	if c.progressHandler != nil {
		t.Fatal("endTurnProgress(genNPlus1) left progressHandler set; want cleared")
	}
	if c.progressRelease != nil {
		t.Fatal("endTurnProgress(genNPlus1) left progressRelease set; want cleared")
	}
}

// TestBeginEndTurnProgress_DoubleReleaseIsNoOp pins ordinary
// double-release safety independent of overlapping turns: calling
// endTurnProgress twice with the SAME (current, valid) generation must
// invoke the registrar's release closure exactly once. The second call
// finds progressGen already matching but progressRelease already nil
// (releaseProgressLocked's own `if c.progressRelease == nil { return }`
// guard), so it is a no-op on the registrar though it still matches
// gen==progressGen - the two guards are independent layers, and this
// test pins the inner one.
func TestBeginEndTurnProgress_DoubleReleaseIsNoOp(t *testing.T) {
	var releaseCount int
	prevRegistrar := SubagentProgressRegistrar
	defer func() { SubagentProgressRegistrar = prevRegistrar }()

	c := NewConversation(nil)
	SubagentProgressRegistrar = func(fn func(agent.Event)) func() {
		return func() { releaseCount++ }
	}

	gen := c.beginTurnProgress(func(agent.Event) {})
	c.endTurnProgress(gen)
	if releaseCount != 1 {
		t.Fatalf("first endTurnProgress(gen) produced releaseCount=%d, want 1", releaseCount)
	}
	// Same (still-current) generation, called again: must not double-fire
	// the registrar's release closure.
	c.endTurnProgress(gen)
	if releaseCount != 1 {
		t.Fatalf("second endTurnProgress(gen) changed releaseCount to %d, want unchanged at 1 (double-release must be a no-op)", releaseCount)
	}
}
