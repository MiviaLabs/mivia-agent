//go:build livechat

package chatsync

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func TestLiveSyncSessionEndToEnd(t *testing.T) {
	ctx := liveContext(t)
	api := newAPI(t, ctx)

	bus := events.New()
	outboxDir := t.TempDir()

	opts := SessionOptions{
		// The live bearer, not the untagged tests' stub: this probe pins what
		// the deployed API does, so it authenticates the way production does.
		TokenProvider: func(context.Context, bool) (string, error) {
			return api.bearer, nil
		},
		ClientOptions:    ClientOptions{BaseURL: api.baseURL},
		OutboxDir:        outboxDir,
		CreateTitle:      "mivia live sync test",
		CwdLabel:         "/workspace/live-test",
		HostLabel:        "live-test-runner",
		PollWaitSeconds:  5,
		HeartbeatPeriod:  2 * time.Second,
		MaxUnflushed:     100,
		EnablePolling:    true,
		ProjectorOptions: ProjectorOptions{IncludeToolIO: true, IncludeThinking: true},
	}

	// The local chat session id: it filters the bus and nothing else. The
	// remote session id the probe reads back below is a different value,
	// assigned by the server.
	const chatSessionID = "live-probe-chat-session"

	syncSess, err := OpenSession(ctx, bus, chatSessionID, opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = syncSess.Stop(stopCtx)
	}()

	publishLiveTurn(bus, chatSessionID, "turn:1")

	// Wait for background flush
	time.Sleep(500 * time.Millisecond)

	// Fetch events directly from server via API to confirm remote persistence
	stored, err := syncSess.client.GetEvents(ctx, syncSess.SessionID(), 0, 50)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}

	if len(stored) < 3 {
		t.Fatalf("expected at least 3 stored events on server, got %d: %+v", len(stored), stored)
	}

	t.Logf("Successfully verified %d live synchronized events on remote session %s", len(stored), syncSess.SessionID())
}

// publishLiveTurn publishes one complete turn on the local bus, stamped with
// the LOCAL chat session id - the only id the projector filters on.
func publishLiveTurn(bus *events.Bus, chatSessionID, turnID string) {
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: chatSessionID,
		TurnID:    turnID,
		Detail:    "live prompt from test",
		Timestamp: time.Now(),
	})
	bus.Publish(events.Event{
		Kind:      events.KindAssistant,
		SessionID: chatSessionID,
		TurnID:    turnID,
		Content:   "live assistant response",
		Timestamp: time.Now(),
	})
	bus.Publish(events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: chatSessionID,
		TurnID:    turnID,
		Detail:    "turn completed",
		Timestamp: time.Now(),
	})
}
