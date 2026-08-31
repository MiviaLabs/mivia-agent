package chatsync

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestProcessEvent_FailedAppendDoesNotLoseDropCount proves the drop watermark
// tracks what the outbox STORED, not what the projector merely built.
//
// checkDrops advances lastDrops when it constructs the sync.dropped marker. If
// the append that would have stored that marker fails, the marker never reaches
// the wire, yet the watermark has already moved - so the NEXT marker reports
// only the loss since the failed one. The hole the wire exists to make visible
// becomes invisible again, and the transcript reads as complete.
func TestProcessEvent_FailedAppendDoesNotLoseDropCount(t *testing.T) {
	rec, srv := newRecordingServer(t, "sess-drop-1")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-drop-1", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Drop Rollback",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	var drops atomic.Uint64
	syncSess.swapDropSource(func() uint64 { return drops.Load() })

	publishTurnStart(bus, "sess-drop-1", "turn:1", "first")
	waitForSeq(t, syncSess, 1)

	// Five events are lost before projection, and the append that would have
	// recorded the marker fails with a plain disk error.
	drops.Store(5)
	a := interceptAppends(syncSess)
	a.fail.Store(true)
	publishTurnStart(bus, "sess-drop-1", "turn:2", "marker lost to a disk error")
	time.Sleep(150 * time.Millisecond)

	// The disk recovers. No further loss occurs, so the next marker must still
	// report all five.
	a.fail.Store(false)
	publishTurnStart(bus, "sess-drop-1", "turn:3", "after recovery")
	time.Sleep(200 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	total := totalDroppedOnWire(t, rec.items())
	if total != 5 {
		t.Errorf("sync.dropped reported %d lost events in total, want 5 "+
			"(a failed marker append must not consume the drop count)", total)
	}
}

// TestProcessEvent_FailedAppendKeepsLaterMarkerHonest is the under-reporting
// case stated as arithmetic: 5 lost, marker append fails, 3 more lost. One
// marker for 8 must reach the wire, not one for 3.
func TestProcessEvent_FailedAppendKeepsLaterMarkerHonest(t *testing.T) {
	rec, srv := newRecordingServer(t, "sess-drop-2")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-drop-2", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Drop Rollback Arithmetic",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	var drops atomic.Uint64
	syncSess.swapDropSource(func() uint64 { return drops.Load() })

	publishTurnStart(bus, "sess-drop-2", "turn:1", "first")
	waitForSeq(t, syncSess, 1)

	drops.Store(5)
	a := interceptAppends(syncSess)
	a.fail.Store(true)
	publishTurnStart(bus, "sess-drop-2", "turn:2", "marker lost")
	time.Sleep(150 * time.Millisecond)

	drops.Store(8)
	a.fail.Store(false)
	publishTurnStart(bus, "sess-drop-2", "turn:3", "after recovery")
	time.Sleep(200 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	total := totalDroppedOnWire(t, rec.items())
	if total != 8 {
		t.Errorf("sync.dropped reported %d lost events in total, want 8 "+
			"(the marker after a failed append must carry the FULL loss)", total)
	}
}

// items returns a copy of every event the recording server received.
func (r *recordingServer) items() []EventItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EventItem, len(r.events))
	copy(out, r.events)
	return out
}

// totalDroppedOnWire sums the `dropped` deltas of every sync.dropped event the
// server received.
func totalDroppedOnWire(t *testing.T, items []EventItem) uint64 {
	t.Helper()
	var total uint64
	for _, it := range items {
		if it.Type != TypeSyncDropped {
			continue
		}
		var p SyncDroppedPayload
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			t.Fatalf("unmarshal sync.dropped payload %s: %v", it.Payload, err)
		}
		total += p.Dropped
	}
	return total
}
