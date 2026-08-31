package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// conflictServer always answers the append endpoint with 409 and counts the
// create, heartbeat and poll traffic so a test can prove they stopped.
type conflictServer struct {
	creates    atomic.Int32
	heartbeats atomic.Int32
	polls      atomic.Int32
}

func newConflictServer(t *testing.T) (*conflictServer, *httptest.Server) {
	t.Helper()
	cs := &conflictServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		n := cs.creates.Add(1)
		id := "sess-409-1"
		if n > 1 {
			id = "sess-409-forked"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: id, Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{
			StatusCode: 409,
			Error:      "Conflict",
			Message:    json.RawMessage(`"session already ended"`),
		})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		cs.heartbeats.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		cs.polls.Add(1)
		select {
		case <-time.After(40 * time.Millisecond):
		case <-r.Context().Done():
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return cs, srv
}

// TestSyncSession_ConflictStopsSyncWithoutForking pins settled decision
// "409 | session ended remotely. Stop pusher, poller, heartbeat. Local chat
// continues" (chat-sync-cli-slice.md:197). A flush 409 must NOT mint a new
// remote session; forking is settled only for the foreign-writer case at
// attach time.
func TestSyncSession_ConflictStopsSyncWithoutForking(t *testing.T) {
	cs, srv := newConflictServer(t)

	bus := events.New()
	ctx := context.Background()

	opts := SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		CreateTitle:     "Conflict Session",
		HeartbeatPeriod: 20 * time.Millisecond,
		EnablePolling:   true,
		PollWaitSeconds: 1,
	}

	syncSess, err := OpenSession(ctx, bus, "sess-409-1", opts)
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
		SessionID: "sess-409-1",
		TurnID:    "turn:1",
		Detail:    "hello",
		Timestamp: time.Now(),
	})

	// Let the flush run and take the 409.
	time.Sleep(300 * time.Millisecond)

	if got := cs.creates.Load(); got != 1 {
		t.Errorf("CreateSession calls = %d, want 1 (a flush 409 must not fork)", got)
	}
	if got := syncSess.SessionID(); got != "sess-409-1" {
		t.Errorf("SessionID() = %q, want %q (session must not be replaced)", got, "sess-409-1")
	}

	// After the 409, heartbeat and poller must be stopped.
	hbBefore := cs.heartbeats.Load()
	pollBefore := cs.polls.Load()
	time.Sleep(300 * time.Millisecond)
	if got := cs.heartbeats.Load(); got != hbBefore {
		t.Errorf("heartbeats kept running after 409: %d -> %d", hbBefore, got)
	}
	if got := cs.polls.Load(); got != pollBefore {
		t.Errorf("poller kept running after 409: %d -> %d", pollBefore, got)
	}
}
