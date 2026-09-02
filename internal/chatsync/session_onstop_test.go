package chatsync

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestSyncSession_OnStopReportsTheReason pins the "stop syncing and SAY SO"
// half of the poison rule. StopReason has always recorded WHY sync stopped,
// but a recorded reason nothing reads is indistinguishable to a user from a
// healthy idle session. OnStop is the push the host surfaces need: no host
// polls SyncSession, so without it the reason can only be found by asking a
// question nobody knows to ask.
func TestSyncSession_OnStopReportsTheReason(t *testing.T) {
	_, srv := newPoisonServer(t)

	var mu sync.Mutex
	var reasons []string

	opts := SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		CreateTitle:     "OnStop Session",
		HeartbeatPeriod: 20 * time.Millisecond,
		EnablePolling:   true,
		PollWaitSeconds: 1,
		OnStop: func(reason string) {
			mu.Lock()
			defer mu.Unlock()
			reasons = append(reasons, reason)
		},
	}

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-poison-1", opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = syncSess.Stop(stopCtx)
	}()

	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-poison-1",
		TurnID:    "turn:1",
		Detail:    "hello",
		Timestamp: time.Now(),
	})
	time.Sleep(400 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 1 {
		t.Fatalf("OnStop calls = %d (%q), want exactly 1; the stop latches once", len(reasons), reasons)
	}
	if !strings.Contains(reasons[0], "400") {
		t.Errorf("OnStop reason = %q, want it to name the 400 that poisoned the stream", reasons[0])
	}
	if got := syncSess.StopReason(); got != reasons[0] {
		t.Errorf("OnStop reason = %q, StopReason() = %q; they must be the same string", reasons[0], got)
	}
}

// TestSyncSession_OnStopIsNotCalledWhileHealthy keeps the notice honest: a
// session that never hits a terminal stop must never claim it did, or the
// warning becomes noise users learn to ignore.
func TestSyncSession_OnStopIsNotCalledWhileHealthy(t *testing.T) {
	api := newFakeAPI(t)

	var calls int
	var mu sync.Mutex
	opts := SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: api.URL()},
		OutboxDir:       t.TempDir(),
		CreateTitle:     "Healthy Session",
		HeartbeatPeriod: 20 * time.Millisecond,
		PollWaitSeconds: 1,
		OnStop: func(string) {
			mu.Lock()
			defer mu.Unlock()
			calls++
		},
	}

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "healthy-1", opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "healthy-1",
		TurnID:    "turn:1",
		Detail:    "hello",
		Timestamp: time.Now(),
	})
	time.Sleep(200 * time.Millisecond)
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = syncSess.Stop(stopCtx)

	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("OnStop calls = %d, want 0; an orderly shutdown is not a sync failure", calls)
	}
}
