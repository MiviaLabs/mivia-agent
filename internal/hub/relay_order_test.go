package hub

import (
	"net"
	"slices"
	"strconv"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// orderScript builds turns worth of the cross-kind traffic a live turn actually
// publishes, with each event's Content set to its publish index so the relay's
// output states exactly what order it observed.
func orderScript(turns int) []events.Event {
	kinds := []events.Kind{
		events.KindTurnStart,
		events.KindThinking,
		events.KindAssistant,
		events.KindAssistant,
		events.KindToolStart,
		events.KindToolEnd,
	}
	out := make([]events.Event, 0, turns*len(kinds))
	for range turns {
		for _, k := range kinds {
			out = append(out, events.Event{
				Kind:      k,
				SessionID: "sess-owner",
				TurnID:    "turn:script",
				Content:   strconv.Itoa(len(out)),
			})
		}
	}
	return out
}

// drainLabels empties c's outbound queue and returns the publish index of each
// queued event, in queue order.
func drainLabels(t *testing.T, c *conn) []int {
	t.Helper()
	out := make([]int, 0, len(c.out))
	for len(c.out) > 0 {
		n, err := strconv.Atoi((<-c.out).Content)
		if err != nil {
			t.Fatalf("relayed an event with unparseable label: %v", err)
		}
		out = append(out, n)
	}
	return out
}

// orderScriptTurns is sized so the whole script fits in connBufSize without the
// connection's drop-oldest queue shedding anything. The assertion is exact
// equality, so a drop would read as a reorder and blame the wrong hop.
const orderScriptTurns = 40

// TestRelayPreservesCrossKindPublishOrder is why internal/hub subscribes with
// SubscribeAcross rather than SubscribeMany.
//
// It asserts at the relay's broadcast boundary, not end to end over the socket,
// because that is where the defect lives. Everything below broadcast is
// order-preserving by construction - one bounded channel per connection, one
// writeLoop draining it, one readLoop decoding it - so an end-to-end test adds
// timing and drop-oldest noise without adding coverage, and in practice does not
// fail when the bus reorders. The bus was the only reordering hop:
// SubscribeMany registers one subscription, with its own queue and its own
// delivery goroutine, PER kind, so N goroutines race into the relay handler and
// a turn's terminal can overtake the deltas it terminates. SubscribeAcross gives
// the whole relayed set one queue.
//
// Flush makes this deterministic rather than timed: it returns only once every
// subscription has delivered everything published before it, under either
// subscription strategy. So both configurations relay all 240 events and the
// only thing that differs is the order.
func TestRelayPreservesCrossKindPublishOrder(t *testing.T) {
	sess := newTestSession(t, "sess-owner")
	o := newOwner(sess, nil)
	o.subscribeRelay()
	c := attachIdleClient(t, o, 1)

	script := orderScript(orderScriptTurns)
	if len(script) > connBufSize {
		t.Fatalf("script of %d exceeds connBufSize %d; drops would be misread as reorders", len(script), connBufSize)
	}
	for _, ev := range script {
		sess.EventBus.Publish(ev)
	}
	sess.EventBus.Flush()

	got := drainLabels(t, c)
	want := make([]int, len(script))
	for i := range want {
		want[i] = i
	}
	if !slices.Equal(got, want) {
		for i := range got {
			if i >= len(want) || got[i] != want[i] {
				t.Fatalf("relay reordered: arrival %d is publish index %d, want %d (got %d of %d events)",
					i, got[i], i, len(got), len(script))
			}
		}
		t.Fatalf("relay dropped events: got %d of %d", len(got), len(script))
	}
}

// TestOwnerStopReleasesTheRelaySubscription pins the other half of the switch:
// once the owner is done, the relay must stop delivering. The per-kind
// Unsubscribe loop this replaced identified the handler by code pointer
// (sameHandler's HandlerFunc fallback), which is best-effort; the handle is
// exact, and it removes the subscription from every relayed kind at once.
func TestOwnerStopReleasesTheRelaySubscription(t *testing.T) {
	sess := newTestSession(t, "sess-owner")
	o := newOwner(sess, nil)
	o.subscribeRelay()

	before := attachIdleClient(t, o, 1)
	sess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-owner", Content: "0"})
	sess.EventBus.Flush()
	if n := len(before.out); n != 1 {
		t.Fatalf("live relay queued %d events for a connected client, want 1", n)
	}

	o.stop()

	// stop() closes and clears every client, so a post-stop delivery needs a
	// fresh one to be observable at all.
	after := attachIdleClient(t, o, 2)
	sess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-owner", Content: "1"})
	sess.EventBus.Flush()
	if n := len(after.out); n != 0 {
		t.Fatalf("relay delivered %d events after stop; the subscription outlived the owner", n)
	}
}

// TestClientStopReleasesTheRelaySubscription is the client-side sibling: the
// forwarding subscription must not outlive the membership either, and the same
// per-kind loop stood behind both.
func TestClientStopReleasesTheRelaySubscription(t *testing.T) {
	sess := newTestSession(t, "sess-client")
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })

	cl := newClient(local, sess, nil)
	cl.subscribeRelay()

	sess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-client", Content: "0"})
	sess.EventBus.Flush()
	if n := len(cl.c.out); n != 1 {
		t.Fatalf("live client relay queued %d events, want 1", n)
	}
	<-cl.c.out

	cl.stop()

	sess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-client", Content: "1"})
	sess.EventBus.Flush()
	if n := len(cl.c.out); n != 0 {
		t.Fatalf("client relay delivered %d events after stop; the subscription outlived the membership", n)
	}
}

// attachIdleClient registers a connection on o with no read/write loops
// running, so whatever broadcast enqueues stays queued and countable.
func attachIdleClient(t *testing.T, o *owner, id uint64) *conn {
	t.Helper()
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
	c := newConn(local)
	o.mu.Lock()
	o.clients[id] = c
	o.mu.Unlock()
	return c
}
