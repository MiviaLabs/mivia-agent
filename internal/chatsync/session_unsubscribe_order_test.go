package chatsync

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// swapUnsubscribe installs a substitute subscription release and returns the
// previous one. Test-only.
func (s *SyncSession) swapUnsubscribe(fn func()) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.unsubscribe
	s.unsubscribe = fn
	return prev
}

// TestSyncSession_UnsubscribeRunsAfterTheFinalDropsRead proves the shutdown
// ordering that keeps a spurious sync.dropped marker out of the transcript.
//
// events.Subscription (internal/events/subscribe_across.go) documents that
// Drops() "read AFTER Unsubscribe can also over-report": a publish already in
// flight still lands in the removed subscription's queue and is dropped there,
// moving the counter without losing anything. Settled decision 6 makes a
// sync.dropped marker permanent, so a marker minted in that window is a
// permanent lie about a hole that never existed.
//
// HONESTY: the over-report is a race inside a concrete type this package does
// not own, so there is no deterministic way to force the spurious marker. This
// test asserts the ORDERING that closes the window - the final Drops() read
// happens while the subscription is still live - and is not a mutation proof
// of the marker itself.
func TestSyncSession_UnsubscribeRunsAfterTheFinalDropsRead(t *testing.T) {
	_, srv := newRecordingServer(t, "sess-unsub-order")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-unsub-order", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Unsubscribe Order",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	var mu sync.Mutex
	var order []string
	record := func(what string) {
		mu.Lock()
		order = append(order, what)
		mu.Unlock()
	}

	realUnsubscribe := syncSess.swapUnsubscribe(func() {
		record("unsubscribe")
	})
	syncSess.swapDropSource(func() uint64 {
		record("drops")
		return 0
	})
	t.Cleanup(realUnsubscribe)

	publishTurnStart(bus, "sess-unsub-order", "turn:1", "content at shutdown")
	waitForSeq(t, syncSess, 1)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	unsubAt := -1
	lastDropsAt := -1
	for i, what := range order {
		switch what {
		case "unsubscribe":
			unsubAt = i
		case "drops":
			lastDropsAt = i
		}
	}
	if unsubAt < 0 {
		t.Fatalf("Unsubscribe never ran; order = %v", order)
	}
	if lastDropsAt < 0 {
		t.Fatalf("the drop counter was never read; order = %v", order)
	}
	if unsubAt < lastDropsAt {
		t.Errorf("Unsubscribe ran at index %d, before the final Drops() read at index %d; "+
			"order = %v. The drop counter must be read while the subscription is live, "+
			"or shutdown can record a permanent sync.dropped for a hole that never existed.",
			unsubAt, lastDropsAt, order)
	}
}
