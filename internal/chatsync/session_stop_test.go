package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// hangFor blocks the handler until d elapses or the client goes away.
func hangFor(w http.ResponseWriter, r *http.Request, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-r.Context().Done():
		return false
	}
}

// newStallingServer answers create and heartbeat immediately but stalls the
// append and long-poll endpoints, which are the two calls a shutdown can hang
// behind.
func newStallingServer(t *testing.T, stall time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-stall", Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		if !hangFor(w, r, stall) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{InsertedCount: 0, LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		if !hangFor(w, r, stall) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestSyncSession_StopHonoursItsContextDeadline pins the Stop contract: the
// caller's deadline bounds shutdown. A parked long poll and a stalled final
// append must not hold the chat process open past it.
func TestSyncSession_StopHonoursItsContextDeadline(t *testing.T) {
	srv := newStallingServer(t, 3*time.Second)

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-stall", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Stop Deadline",
		HeartbeatPeriod: 10 * time.Minute,
		EnablePolling:   true,
		PollWaitSeconds: 25,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	// Give the poller time to park on the stalled long poll and queue an event
	// so the shutdown drain has real work.
	publishTurnStart(bus, "sess-stall", "turn:1", "pending at shutdown")
	time.Sleep(150 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_ = syncSess.Stop(stopCtx)
	elapsed := time.Since(start)
	select {
	case <-syncSess.shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed-out Stop did not finish asynchronous shutdown before test cleanup")
	}

	if elapsed > 1*time.Second {
		t.Errorf("Stop took %v under a 200ms deadline; want it to return within 1s", elapsed)
	}
}
