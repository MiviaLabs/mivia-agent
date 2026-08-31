package hub

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// newIdleConn returns a conn with no read/write loops running, so its outbound
// queue fills and its drop path is reachable deterministically.
func newIdleConn(t *testing.T) *conn {
	t.Helper()
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
	return newConn(local)
}

// TestConnCountsWhatItDrops is the base of the loss contract. conn.send has
// always dropped the oldest queued event when the writer falls behind, and did
// it with NO counter at all - internal/events at least exposes Drops(), while
// this hop shed events silently, so a consumer could not tell a short answer
// from a truncated one.
func TestConnCountsWhatItDrops(t *testing.T) {
	c := newIdleConn(t)

	for range connBufSize {
		c.send(WireEvent{Kind: "assistant"})
	}
	if n := c.Dropped(); n != 0 {
		t.Fatalf("dropped %d events while filling the queue exactly to capacity, want 0", n)
	}

	const overflow = 7
	for range overflow {
		c.send(WireEvent{Kind: "assistant"})
	}
	if n := c.Dropped(); n != overflow {
		t.Fatalf("dropped = %d after %d events past capacity, want %d", n, overflow, overflow)
	}
	if len(c.out) != connBufSize {
		t.Fatalf("queue holds %d events, want it pinned at capacity %d", len(c.out), connBufSize)
	}
}

// TestSendStampsAMonotonicLossCount is the property a consumer actually relies
// on: it detects loss by diffing this number, so the number must never fall.
//
// The obvious implementation - stamp the count only on the drop path - is
// wrong, and this is the test that says so. It leaves every event sent while
// the queue has room reporting the upstream-only value, so the moment a slow
// reader catches up the count drops back and a consumer diffing it reads the
// recovery as "no loss".
func TestSendStampsAMonotonicLossCount(t *testing.T) {
	c := newIdleConn(t)

	// Overflow the queue so the connection takes real losses.
	for range connBufSize + 5 {
		c.send(WireEvent{Kind: "assistant"})
	}
	if c.Dropped() == 0 {
		t.Fatal("no drops recorded; this test cannot say anything about the count")
	}

	// Drain fully, so the next send takes the has-room path.
	for len(c.out) > 0 {
		<-c.out
	}
	c.send(WireEvent{Kind: "assistant"})

	got := (<-c.out).Dropped
	if got < c.Dropped() {
		t.Fatalf("an event sent after recovery reports Dropped=%d, below the connection's total %d; the count went backwards", got, c.Dropped())
	}
}

// TestSendAddsUpstreamLossToItsOwn pins that the two loss points compose. The
// relay's shared bus queue stamps its count before send is reached; send must
// add this connection's own rather than overwrite it, or a consumer sees only
// the last hop's loss.
func TestSendAddsUpstreamLossToItsOwn(t *testing.T) {
	c := newIdleConn(t)
	for range connBufSize + 3 {
		c.send(WireEvent{Kind: "assistant"})
	}
	own := c.Dropped()
	for len(c.out) > 0 {
		<-c.out
	}

	const upstream = 11
	c.send(WireEvent{Kind: "assistant", Dropped: upstream})

	if got := (<-c.out).Dropped; got != upstream+own {
		t.Fatalf("Dropped = %d, want upstream %d + this connection's %d", got, upstream, own)
	}
}

// stallRelayAndOverflow blocks the relay's delivery goroutine by holding the
// lock its handler needs, publishes past the bus queue's capacity so the
// subscription really sheds events, then releases it. It returns the number of
// drops the subscription took, which the caller asserts is non-zero - a test
// that compares 0 to 0 cannot see this feature at all.
func stallRelayAndOverflow(t *testing.T, sess *chat.Session, o *owner) uint64 {
	t.Helper()
	o.mu.Lock()
	for range relayBufSize * 2 {
		sess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-owner", Content: "x"})
	}
	o.mu.Unlock()
	sess.EventBus.Flush()
	drops := o.sub.Drops()
	if drops == 0 {
		t.Fatalf("the relay subscription dropped nothing after %d publishes; this test cannot see the stamp", relayBufSize*2)
	}
	return drops
}

// TestRelayStampsTheBusQueuesLoss pins that an event which passed through the
// relay subscription carries THAT queue's loss.
//
// It previously asserted got == o.sub.Drops() with both sides zero, and so
// passed with the stamp deleted outright, with ref.Store removed, and with the
// client-side stamp deleted. A drop is forced here so the expected value is
// non-zero and the assertion has something to fail on.
func TestRelayStampsTheBusQueuesLoss(t *testing.T) {
	sess := newTestSession(t, "sess-owner")
	o := newOwner(sess, nil)
	o.subscribeRelay()
	c := attachIdleClient(t, o, 1)

	drops := stallRelayAndOverflow(t, sess, o)

	var sawStamped bool
	for len(c.out) > 0 {
		if (<-c.out).Dropped >= drops {
			sawStamped = true
		}
	}
	if !sawStamped {
		t.Fatalf("no relayed event reported the relay queue's %d drops; the stamp is not being read", drops)
	}
}

// TestRelayChargesBusLossOnlyToEventsThatPassedThroughIt keeps the number
// honest about WHICH loss it reports, through the REAL rebroadcast path.
//
// The earlier version called o.broadcast directly with Dropped unset, so it
// asserted 0 == 0 against a fixture that zeroed the very field production
// populates - a permissive fake. Production decodes a peer's WireEvent, which
// carries that peer's own cumulative total, and re-fans it. Forwarding that
// number makes the receiving consumer's counter fall, because its stream then
// interleaves two unrelated counter origins.
func TestRelayChargesBusLossOnlyToEventsThatPassedThroughIt(t *testing.T) {
	sess := newTestSession(t, "sess-owner")
	o := newOwner(sess, nil)
	o.subscribeRelay()
	// A high id, because accept() mints its own from nextID starting at 1 and
	// would otherwise replace this entry in o.clients.
	dest := attachIdleClient(t, o, 99)

	// A real source connection: accept() decodes from the socket exactly as it
	// does for a live peer.
	srcLocal, srcRemote := net.Pipe()
	t.Cleanup(func() { _ = srcLocal.Close(); _ = srcRemote.Close() })
	o.accept(srcLocal)

	const foreign = 50
	line, err := json.Marshal(WireEvent{Kind: "assistant", SessionID: "sess-owner", Dropped: foreign})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srcRemote.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 5*time.Second, func() bool { return len(dest.out) > 0 })
	if got := (<-dest.out).Dropped; got >= foreign {
		t.Fatalf("a rebroadcast carried the SOURCE peer's counter (%d); the destination's counter can then fall", got)
	}
}

// TestOwnerSinkAccumulatesLossPerSource is the other half of that defect. The
// owner's own sink is fed by every connected client, each with an independent
// counter, so handing it whichever absolute number arrived last makes the value
// oscillate. It must fold per-source deltas into one total instead.
func TestOwnerSinkAccumulatesLossPerSource(t *testing.T) {
	var got []uint64
	var mu sync.Mutex
	sess := newTestSession(t, "sess-owner")
	o := newOwner(sess, func(_ events.Event, r Receipt) {
		mu.Lock()
		got = append(got, r.Dropped)
		mu.Unlock()
	})
	o.subscribeRelay()

	// Client A has already shed 40; client B has shed nothing.
	for _, step := range []struct {
		id       uint64
		reported uint64
	}{{1, 40}, {2, 0}, {1, 45}, {2, 3}} {
		o.sink(events.Event{}, Receipt{Dropped: o.noteSourceLoss(step.id, step.reported)})
	}

	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Fatalf("owner sink loss fell from %d to %d; two clients' counters were mixed: %v", got[i-1], got[i], got)
		}
	}
	if want := uint64(48); got[len(got)-1] != want {
		t.Fatalf("accumulated total = %d, want %d (A: 40 then +5, B: 0 then +3)", got[len(got)-1], want)
	}
}
