package chatsync

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestHandleEvent_DoesNotBlockOnOutboxIO pins the settled contract that the
// bus handler "does a non-blocking send and nothing else"
// (chat-sync-cli-slice.md:90, :180) and "never blocks the local chat" (:201).
//
// events.Bus.Publish runs handlers on the subscription's delivery goroutine, so
// a handler that waits on disk stalls the whole local event stream behind an
// fsync. The handler took s.mu, and processEvent held s.mu across the outbox
// append that fsyncs - so a slow disk blocked the bus.
func TestHandleEvent_DoesNotBlockOnOutboxIO(t *testing.T) {
	_, srv := newRecordingServer(t, "sess-handler")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-handler", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Handler Latency",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = syncSess.Stop(stopCtx)
	}()

	// Model a disk slow enough to notice. The release is timed, not manual, so
	// a regression stalls the assertion instead of deadlocking the suite.
	const stallFor = 600 * time.Millisecond
	a := interceptAppends(syncSess)
	stall := make(chan struct{})
	a.stall.Store(&stall)
	time.AfterFunc(stallFor, func() { close(stall) })

	publishTurnStart(bus, "sess-handler", "turn:1", "stalls the writer")

	select {
	case <-a.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the outbox write never started; test cannot measure handler latency")
	}

	start := time.Now()
	syncSess.HandleEvent(context.Background(), events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-handler",
		TurnID:    "turn:2",
		Detail:    "must be accepted without waiting for the disk",
		Timestamp: time.Now(),
	})
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("HandleEvent took %v while the outbox write was stalled for %v; "+
			"the bus handler must never wait on disk I/O", elapsed, stallFor)
	}

	// The fast return must be a real enqueue, not a dropped event.
	waitForSeq(t, syncSess, 2)
}
