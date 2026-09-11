// conversation_live_internal_test.go covers the fan-out behaviors that
// need package-private access: overflow marks a viewer stale once and
// then drops its events instead of blocking the producer.
package uiadapter

import (
	"testing"
	"time"

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
