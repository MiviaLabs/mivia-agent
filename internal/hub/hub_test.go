package hub

import (
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
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
	mu  sync.Mutex
	evs []events.Event
}

func (c *collector) sink(ev events.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evs = append(c.evs, ev)
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

	waitFor(t, 2*time.Second, func() bool {
		ownerSess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-owner", TurnID: "turn:1", Content: "hello from owner"})
		return clientSink.count() > 0
	})
	if got := clientSink.last(); got.Content != "hello from owner" {
		t.Fatalf("client sink got %+v, want content %q", got, "hello from owner")
	}

	waitFor(t, 2*time.Second, func() bool {
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

	waitFor(t, 2*time.Second, func() bool {
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
// takeover happened; retrying the publish inside waitFor covers both the
// backoff delay and the newcomer's own connect time without a fixed sleep.
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
	waitFor(t, 2*time.Second, func() bool {
		ownerSess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-owner", TurnID: "turn:0", Content: "warmup"})
		return survivorSink.count() > 0
	})

	ownerHandle.Leave() // release the lock and close the listener

	newcomerSess := newTestSession(t, "sess-newcomer")
	newcomerSink := &collector{}
	newcomerHandle := Join(dir, newcomerSess, newcomerSink.sink)
	defer newcomerHandle.Leave()

	waitFor(t, reconnectBackoff+3*time.Second, func() bool {
		survivorSess.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "sess-survivor", TurnID: "turn:1", Content: "still alive"})
		return newcomerSink.count() > 0
	})
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
