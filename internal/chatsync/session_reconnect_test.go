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

// TestSessionRecoversAutomaticallyAfterAHungFlush is the reported bug's
// regression test: a backend container restart typically black-holes the
// connection (the draining pod stops responding instead of sending RST)
// rather than failing fast, so the events endpoint here sleeps past the
// client's own request deadline instead of answering - exactly what a hung
// TCP connection looks like from execRequest's side, and a case
// TestFlush_TransientFailureBacksOff's fast-500 server doesn't cover.
//
// No external Stop/OpenSession call is made after the hang clears: recovery
// has to be automatic, or this test times out.
func TestSessionRecoversAutomaticallyAfterAHungFlush(t *testing.T) {
	orig := defaultRequestTimeout
	defaultRequestTimeout = 150 * time.Millisecond
	defer func() { defaultRequestTimeout = orig }()

	var mu sync.Mutex
	attempts := 0
	delivered := false
	hangUntil := time.Now().Add(600 * time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-reconnect", Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		if time.Now().Before(hangUntil) {
			// Outlive the client's own (shrunk) request timeout so Do()
			// gives up via context deadline, not a fast server error.
			time.Sleep(defaultRequestTimeout + 150*time.Millisecond)
			return
		}
		mu.Lock()
		delivered = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{InsertedCount: 1, LastSeq: 1})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-reconnect", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Reconnect",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	publishTurnStart(bus, "sess-reconnect", "turn:1", "resumes after the hang clears")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := delivered
		mu.Unlock()
		if ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = syncSess.Stop(stopCtx)

	mu.Lock()
	defer mu.Unlock()
	if !delivered {
		t.Fatalf("sync never recovered after the hung flush cleared (%d attempts made) - no external Stop/OpenSession call was made, so recovery must be automatic", attempts)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, want at least 2 (one hung then timed out, one after recovery) - too few attempts means the retry/backoff path was never actually exercised by the hang", attempts)
	}
}
