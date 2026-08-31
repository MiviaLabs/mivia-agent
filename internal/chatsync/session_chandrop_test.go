package chatsync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestHandleEvent_ChannelDropsReachTheDroppedMarker covers the SECOND loss hop.
//
// HandleEvent does a non-blocking send onto eventCh and discards the event on
// `default:`. sync.dropped was fed only from Subscription.Drops(), the BUS
// counter, so a drop at this hop produced a contiguous, complete-LOOKING
// transcript that was silently missing content - exactly the failure settled
// decision 6 exists to make visible, reproduced one layer down.
//
// The seam is SessionOptions.EventBufSize: the internal channel is 1024 deep in
// production, which no test can fill honestly. Here it is 1 and the outbox
// writer is stalled, so the loss is real and the count is the code's own, never
// hand-fed.
func TestHandleEvent_ChannelDropsReachTheDroppedMarker(t *testing.T) {
	rec, srv := newRecordingServer(t, "sess-chandrop")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-chandrop", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    1000,
		CreateTitle:     "Channel Drops",
		HeartbeatPeriod: 10 * time.Minute,
		EventBufSize:    1,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	// Stall the writer so the worker cannot drain eventCh.
	a := interceptAppends(syncSess)
	stall := make(chan struct{})
	a.stall.Store(&stall)

	const published = 40
	for i := 0; i < published; i++ {
		publishTurnStart(bus, "sess-chandrop", "turn:1", "burst")
	}
	select {
	case <-a.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the outbox write never started")
	}
	// Let the delivery goroutine push the rest of the burst at the full channel.
	time.Sleep(200 * time.Millisecond)
	close(stall)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// The bus itself shed nothing (its queue is 1024 deep), so any reported
	// loss can only have come from the channel hop under test.
	if busDropped := syncSess.sub.Drops(); busDropped != 0 {
		t.Fatalf("the bus dropped %d events; this test must isolate the channel hop", busDropped)
	}

	var total uint64
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, ev := range rec.events {
		if ev.Type != TypeSyncDropped {
			continue
		}
		var p SyncDroppedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode %s payload: %v", TypeSyncDropped, err)
		}
		if p.TotalDropped > total {
			total = p.TotalDropped
		}
	}

	if total == 0 {
		t.Fatalf("no %s marker recorded a channel drop; %d of %d events were published "+
			"into a 1-deep channel behind a stalled writer, so the transcript is "+
			"silently incomplete", TypeSyncDropped, published, published)
	}
}
