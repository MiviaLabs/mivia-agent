package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestFlush_TransientFailureBacksOff pins the settled retry policy: "keep the
// batch at the outbox head, jittered backoff 250ms -> 30s"
// (chat-sync-cli-slice.md:194).
//
// The code retried on a fixed 100ms ticker, so an identical failing body was
// resubmitted at 10Hz forever - a retry storm aimed at a server that is
// already failing.
func TestFlush_TransientFailureBacksOff(t *testing.T) {
	var mu sync.Mutex
	var attempts []time.Time

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-backoff", Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts = append(attempts, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"statusCode":500,"error":"Internal Server Error"}`))
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-backoff", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Backoff",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	publishTurnStart(bus, "sess-backoff", "turn:1", "never lands")

	const window = 1200 * time.Millisecond
	time.Sleep(window)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = syncSess.Stop(stopCtx)

	mu.Lock()
	defer mu.Unlock()

	if len(attempts) == 0 {
		t.Fatal("no append attempt was made at all")
	}
	// 250ms -> 500ms -> 1000ms, halved at worst by jitter, plus the final
	// shutdown flush. A fixed 100ms ticker produces about 12.
	const maxAttempts = 7
	if len(attempts) > maxAttempts {
		t.Errorf("%d append attempts in %v; want at most %d. A fixed-interval retry "+
			"resubmits an identical failing body at 10Hz forever",
			len(attempts), window, maxAttempts)
	}
	if len(attempts) >= 2 {
		gap := attempts[1].Sub(attempts[0])
		// jitter floor is half the 250ms minimum.
		if gap < 100*time.Millisecond {
			t.Errorf("first retry gap = %v; want at least the jittered 250ms minimum", gap)
		}
	}
}
