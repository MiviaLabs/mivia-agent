package chatsync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestAppendFailureIsReportedAsDroppedLoss proves an event lost at the DURABLE
// APPEND hop still reaches a reader as a hole.
//
// The two upstream loss hops - the bus's drop-oldest queue and this session's
// handler-to-worker channel - both feed a sync.dropped marker. The append hop
// did not: processEvent rolled the seq back and returned, so the transcript
// stayed contiguous and complete-LOOKING with content missing from the middle.
// That is the one failure mode a lossy wire must never have, and it is exactly
// the mode a slow or offline uplink reaches first, because the bounded outbox
// fills long before either in-memory queue does.
func TestAppendFailureIsReportedAsDroppedLoss(t *testing.T) {
	rec, srv := newRecordingServer(t, "sess-append-drop")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-append-drop", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Append Drop",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = syncSess.Stop(context.Background()) })

	publishTurnStart(bus, "sess-append-drop", "turn:1", "first")
	waitForSeq(t, syncSess, 1)

	// One event is projected and then lost at the append.
	appender := interceptAppends(syncSess)
	appender.fail.Store(true)
	publishTurnStart(bus, "sess-append-drop", "turn:2", "lost")
	waitForAppendDrops(t, syncSess, 1)

	// The next event that DOES store must carry the marker for it.
	appender.fail.Store(false)
	publishTurnStart(bus, "sess-append-drop", "turn:3", "after")

	waitForDroppedMarker(t, rec)
}

// TestAppendFailureDropCountIsPerSourceEvent proves the reported hole counts
// SOURCE events, matching the two upstream counters. Counting the wire events
// a projection produced would overstate the loss, and a reader comparing the
// marker against the run's own event count would read a hole that is not there.
func TestAppendFailureDropCountIsPerSourceEvent(t *testing.T) {
	_, srv := newRecordingServer(t, "sess-append-count")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-append-count", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Append Drop Count",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = syncSess.Stop(context.Background()) })

	publishTurnStart(bus, "sess-append-count", "turn:1", "first")
	waitForSeq(t, syncSess, 1)

	appender := interceptAppends(syncSess)
	appender.fail.Store(true)
	for i := range 3 {
		publishTurnStart(bus, "sess-append-count", "turn:lost", string(rune('a'+i)))
	}
	waitForAppendDrops(t, syncSess, 3)
	appender.fail.Store(false)

	if got := syncSess.appendDrops.Load(); got != 3 {
		t.Fatalf("appendDrops = %d, want 3 - one per lost source event", got)
	}
}

// waitForAppendDrops blocks until the session has counted want append-hop
// losses, or fails the test.
func waitForAppendDrops(t *testing.T, s *SyncSession, want uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.appendDrops.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("appendDrops = %d, want at least %d", s.appendDrops.Load(), want)
}

// waitForDroppedMarker blocks until the recording server has received a
// sync.dropped event reporting a non-zero hole.
func waitForDroppedMarker(t *testing.T, rec *recordingServer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		stored := append([]EventItem(nil), rec.events...)
		rec.mu.Unlock()
		for _, e := range stored {
			if e.Type != TypeSyncDropped {
				continue
			}
			var payload struct {
				Dropped uint64 `json:"dropped"`
			}
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("decode sync.dropped payload: %v", err)
			}
			if payload.Dropped == 0 {
				t.Fatalf("sync.dropped reported 0 events; a marker for no loss says nothing")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no sync.dropped marker reached the server; append-hop loss stayed silent")
}
