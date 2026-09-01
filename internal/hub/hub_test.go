package hub

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func newTestSession(t *testing.T, sessionID string) *chat.Session {
	t.Helper()
	sess := chat.NewSession(&config.Resolved{}, nil)
	sess.SessionID = sessionID
	sess.EventBus = events.New()
	return sess
}

// waitFor polls cond (via a ticker, not a fixed sleep) until it returns true
// or timeout elapses, failing the test on timeout. Every hub interaction
// crosses a real goroutine/socket boundary with no synchronous "connected"
// signal exposed to tests, so callers whose cond has a side effect (e.g.
// re-publishing an event) use this to retry until that side effect actually
// lands rather than guessing how long setup takes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("condition not met within %s", timeout)
		}
	}
}

// collector is a concurrency-safe events.Event sink for assertions.
type collector struct {
	mu      sync.Mutex
	evs     []events.Event
	dropped uint64
}

func (c *collector) sink(ev events.Event, r Receipt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evs = append(c.evs, ev)
	c.dropped = r.Dropped
}

// lastDropped returns the cumulative loss count from the most recent receipt.
func (c *collector) lastDropped() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.evs)
}

func (c *collector) last() events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evs[len(c.evs)-1]
}

// any reports whether any collected event satisfies f, so a wait can
// correlate on the ONE event that proves a claim instead of on arrival count,
// which stale deliveries from a previous hub epoch can satisfy too.
func (c *collector) any(f func(events.Event) bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.evs {
		if f(ev) {
			return true
		}
	}
	return false
}

func TestTryAcquireLock(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/hub.lock"

	lock, ok := tryAcquireLock(path)
	if !ok {
		t.Fatal("expected first acquire to succeed")
	}
	if _, ok := tryAcquireLock(path); ok {
		t.Fatal("expected second acquire to fail while first is held")
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	lock2, ok := tryAcquireLock(path)
	if !ok {
		t.Fatal("expected acquire to succeed after unlock")
	}
	_ = lock2.Unlock()
}

// TestJoinOwnerToClientBroadcast: the first Join call for a workspace
// becomes the owner; a second Join call for the same workspace becomes a
// client. An event published on the owner's own EventBus must reach the
// client's sink, and vice versa. Publishing is retried inside waitFor
// (rather than sleeping first) since Join returns before the background
// dial/accept/subscribe handshake completes, and a publish that lands
// before the client connects is simply not queued for it.
func TestJoinOwnerToClientBroadcast(t *testing.T) {
	dir := t.TempDir()
	ownerSess := newTestSession(t, "sess-owner")
	clientSess := newTestSession(t, "sess-client")
	ownerSink := &collector{}
	clientSink := &collector{}

	ownerHandle := Join(dir, ownerSess, ownerSink.sink)
	defer ownerHandle.Leave()
	clientHandle := Join(dir, clientSess, clientSink.sink)
	defer clientHandle.Leave()

	// 5s, not 2s: a cold test-binary invocation's first flock/socket bind
	// has occasionally needed longer than 2s in this sandbox (observed
	// flake, not a correctness issue - every subsequent case in the same
	// process runs in well under 100ms).
	waitFor(t, 5*time.Second, func() bool {
		ownerSess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-owner", TurnID: "turn:1", Content: "hello from owner"})
		return clientSink.count() > 0
	})
	if got := clientSink.last(); got.Content != "hello from owner" {
		t.Fatalf("client sink got %+v, want content %q", got, "hello from owner")
	}

	waitFor(t, 5*time.Second, func() bool {
		clientSess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-client", TurnID: "turn:1", Content: "hello from client"})
		return ownerSink.count() > 0
	})
	if got := ownerSink.last(); got.Content != "hello from client" {
		t.Fatalf("owner sink got %+v, want content %q", got, "hello from client")
	}
}

// TestBroadcastExcludesOrigin: with one owner and two clients, an event
// published by client A must reach client B and the owner, but must not
// echo back to client A itself. owner.broadcast excludes the origin
// connection synchronously within the same call that enqueues delivery to
// everyone else, so there is no later window in which a delayed echo could
// still arrive - checking aSink immediately after b/owner receive it is
// deterministic, not a race that happens to usually pass.
func TestBroadcastExcludesOrigin(t *testing.T) {
	dir := t.TempDir()
	ownerSess := newTestSession(t, "sess-owner")
	aSess := newTestSession(t, "sess-a")
	bSess := newTestSession(t, "sess-b")
	ownerSink, aSink, bSink := &collector{}, &collector{}, &collector{}

	ownerHandle := Join(dir, ownerSess, ownerSink.sink)
	defer ownerHandle.Leave()
	aHandle := Join(dir, aSess, aSink.sink)
	defer aHandle.Leave()
	bHandle := Join(dir, bSess, bSink.sink)
	defer bHandle.Leave()

	waitFor(t, 5*time.Second, func() bool {
		aSess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-a", TurnID: "turn:1", Content: "from a"})
		return bSink.count() > 0 && ownerSink.count() > 0
	})

	if aSink.count() != 0 {
		t.Fatalf("origin client received its own event back: %+v", aSink.evs)
	}
}

// TestReconnectAfterOwnerExit: when the owner leaves, a client loses its
// connection and, per membershipLoop's retry, re-attempts election after
// reconnectBackoff - since the lock is now free, it becomes the new owner.
// A third process joining and exchanging an event with it proves the
// takeover happened; the wait correlates on the SURVIVOR's event (session,
// turn and content) because Leave's asynchronous unwind lets the newcomer
// receive stale events - the warmup - from the dying old hub, and retrying
// the publish inside waitFor covers both the backoff delay and the
// newcomer's own connect time without a fixed sleep.
func TestReconnectAfterOwnerExit(t *testing.T) {
	dir := t.TempDir()
	ownerSess := newTestSession(t, "sess-owner")
	survivorSess := newTestSession(t, "sess-survivor")
	survivorSink := &collector{}

	ownerHandle := Join(dir, ownerSess, nil)
	survivorHandle := Join(dir, survivorSess, survivorSink.sink)
	defer survivorHandle.Leave()
	// Prove the survivor is actually connected as a client before pulling
	// the owner out from under it, so the reconnect this test is about is
	// unambiguous rather than "it never connected to begin with."
	waitFor(t, 5*time.Second, func() bool {
		ownerSess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-owner", TurnID: "turn:0", Content: "warmup"})
		return survivorSink.count() > 0
	})

	ownerHandle.Leave() // release the lock and close the listener

	// Leave only cancels the owner's context; the unwind - close the
	// listener, drop every client connection, release the lock - happens
	// asynchronously on the owner's goroutine. Two consequences the wait
	// below must survive:
	//
	//  1. The newcomer can dial the DYING hub inside that window and receive
	//     events the old owner had already relayed (the warmup). Such stale
	//     deliveries say nothing about a takeover, so the wait correlates on
	//     the survivor's event instead of on arrival count - count()>0 used
	//     to be satisfied by exactly that stale warmup under load.
	//  2. There is no stable "old hub is gone" signal to barrier on: the
	//     lock is free only between the old owner's release and the
	//     survivor's next election attempt, and is then held for the LIFE of
	//     the new owner - so waiting for lock-freeness deadlocks the test.
	//     The budget is two backoff cycles plus dial and relay time: worst
	//     case is the survivor detecting the death late, sleeping
	//     reconnectBackoff, and a slow dial.
	newcomerSess := newTestSession(t, "sess-newcomer")
	newcomerSink := &collector{}
	newcomerHandle := Join(dir, newcomerSess, newcomerSink.sink)
	defer newcomerHandle.Leave()

	waitFor(t, 2*reconnectBackoff+3*time.Second, func() bool {
		survivorSess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-survivor", TurnID: "turn:1", Content: "still alive"})
		return newcomerSink.any(func(ev events.Event) bool {
			return ev.SessionID == "sess-survivor" && ev.TurnID == "turn:1" && ev.Content == "still alive"
		})
	})
	// The correlated wait above IS the takeover assertion - only a hub the
	// survivor now feeds (as its owner, or as a client of the
	// newcomer-owned hub) can deliver an event with that identity, and a
	// stale warmup from the dying old hub cannot match it. Deliberately NO
	// last()-style assertion here: a warmup straggler from the dying hub can
	// legitimately land after the survivor's event, and whichever event is
	// last says nothing about whether the takeover happened.
}

func TestToWireFromWireRoundTrip(t *testing.T) {
	original := events.Event{
		Kind: events.KindToolStart, SessionID: "s1", TurnID: "t1", ToolCallID: "tc1",
		Name: "read_file", Content: "c", Input: "in", Output: "out",
		AgentTask: "task1", AgentName: "agent1", AgentDepth: 2,
	}
	got := fromWire(toWire(original))
	if got.Kind != original.Kind || got.SessionID != original.SessionID || got.TurnID != original.TurnID ||
		got.ToolCallID != original.ToolCallID || got.Name != original.Name || got.Content != original.Content ||
		got.Input != original.Input || got.Output != original.Output || got.AgentTask != original.AgentTask ||
		got.AgentName != original.AgentName || got.AgentDepth != original.AgentDepth {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, original)
	}
}

// TestCompactionRelayRoundTrip pins the compaction relay contract: the typed,
// content-free payload survives toWire/fromWire (a desktop sidecar observing
// another process's session gets the real before/after numbers, not a Detail
// string to parse), and KindCompaction is in relayedKinds - without the
// relayedKinds entry the hub drops the event entirely, cross-process.
func TestCompactionRelayRoundTrip(t *testing.T) {
	if !slices.Contains(relayedKinds, events.KindCompaction) {
		t.Fatal("KindCompaction missing from relayedKinds: the hub drops compaction events cross-process")
	}
	typed, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 10_000, AfterTokens: 3_000,
		ElidedMessages: 5, ElidedBytes: 4_200,
		SourceRange: contextstate.SourceRange{
			Start: contextstate.SourceID{SessionID: "s1", Sequence: 1},
			End:   contextstate.SourceID{SessionID: "s1", Sequence: 40},
		},
		SummaryVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	original := events.Event{
		Kind: events.KindCompaction, SessionID: "s1", TurnID: "turn:3",
		Detail: "context compacted: 10000 -> 3000 tokens", Compaction: &typed,
	}
	got := fromWire(toWire(original))
	if got.Kind != events.KindCompaction || got.SessionID != original.SessionID || got.TurnID != original.TurnID {
		t.Fatalf("scalar fields mismatch: got %+v", got)
	}
	if got.Compaction == nil {
		t.Fatal("compaction payload dropped on the wire round trip")
	}
	if got.Compaction.BeforeTokens != 10_000 || got.Compaction.AfterTokens != 3_000 ||
		got.Compaction.ElidedMessages != 5 || got.Compaction.ElidedBytes != 4_200 ||
		got.Compaction.Trigger != "threshold" || got.Compaction.SummaryVersion != 1 {
		t.Fatalf("compaction payload mismatch: %+v", got.Compaction)
	}
}
