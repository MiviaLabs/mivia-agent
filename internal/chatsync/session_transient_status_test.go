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

// newCountingRejectingServer is newRejectingServer with an append counter, so a
// test can prove a RETRY happened rather than merely that nothing stopped. A
// session that never reaches the append at all is also "not stopped", and that
// is the reading this counter rules out.
func newCountingRejectingServer(t *testing.T, status int, appends *atomic.Int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-transient", Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, _ *http.Request) {
		appends.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "try again later"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestTransientStatusesKeepRetrying pins the OTHER half of the poison rule
// (DC-28). parseErrorResponse names 400, 413 and 422 as poison and stops sync
// terminally on them; 408 and 429 are the server asking for a later retry, and
// classifying either as poison would convert a transient slowdown - a rate
// limit, a slow upstream - into a permanent stop that only a process restart
// undoes.
//
// TestFlush_NonRetryableClientErrorsStopSync asserts the poison half. Nothing
// asserted this half, so widening the poison case list to "every 4xx" - the
// obvious-looking generalisation, and the one a reader of that test would make -
// passed the whole suite.
//
// The counter is load-bearing. Asserting only !Stopped() would also pass if the
// flush never ran, so the test first waits for repeated appends against the
// rejecting server and only then reads Stopped().
func TestTransientStatusesKeepRetrying(t *testing.T) {
	const wantAttempts = 3

	for _, tc := range []struct {
		name   string
		status int
	}{
		{"408 request timeout", http.StatusRequestTimeout},
		{"429 too many requests", http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var appends atomic.Int64
			srv := newCountingRejectingServer(t, tc.status, &appends)
			bus := events.New()
			s, err := OpenSession(context.Background(), bus, "chat-transient", SessionOptions{
				TokenProvider:   testTokenProvider,
				ClientOptions:   ClientOptions{BaseURL: srv.URL},
				OutboxDir:       t.TempDir(),
				MaxUnflushed:    100,
				CreateTitle:     "Transient Status",
				HeartbeatPeriod: 10 * time.Minute,
			})
			if err != nil {
				t.Fatalf("OpenSession: %v", err)
			}
			t.Cleanup(func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = s.Stop(stopCtx)
			})

			publishTurnStart(bus, "chat-transient", "turn:1", "content the server is busy for")

			waitUntil(t, "the append to be retried after a transient rejection", func() bool {
				return appends.Load() >= wantAttempts || s.Stopped()
			})

			if s.Stopped() {
				t.Fatalf("status %d stopped sync terminally (reason %q); the server asked for a retry, and latching a permanent stop on it needs a process restart to undo", tc.status, s.StopReason())
			}
			if got := appends.Load(); got < wantAttempts {
				t.Fatalf("append attempts = %d, want >= %d; sync neither stopped nor retried, so this test proves nothing about the retry path", got, wantAttempts)
			}
		})
	}
}
