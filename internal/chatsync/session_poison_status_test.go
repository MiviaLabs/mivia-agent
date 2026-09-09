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

// newRejectingServer answers create normally and then rejects every append
// with status, so a test can drive one non-retryable client error end to end.
func newRejectingServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-poison", Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "payload too large"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestFlush_NonRetryableClientErrorsStopSync is the fail-safe for the poison
// rule, and it is deliberately about statuses the deployed API does NOT
// currently return.
//
// TestLiveChatSessionPayloadBoundIsAClientError pins the real server at 400
// for an oversized payload, and classify has always handled 400. But it
// handled ONLY 400: 413 and 422 fell to the default branch, which produces a
// plain error rather than ErrBadRequest, so flushNow routed them to
// scheduleRetry and the session re-sent a body the server can never accept -
// on the flush ticker, for the life of the process, with no stop and nothing
// said. The contract calls that out as the one outcome worse than failing
// (chat-sync-event-contract.md:285-287: stop syncing and SAY SO).
//
// So this test does not assert today's behaviour of the API. It asserts that
// the client survives the API changing its mind, which is the whole reason a
// classification exists rather than a single equality check.
//
// 408 and 429 are deliberately absent: those are the server asking for a
// retry, and poisoning them would convert a transient slowdown into a
// permanent stop.
func TestFlush_NonRetryableClientErrorsStopSync(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"413 payload too large", http.StatusRequestEntityTooLarge},
		{"422 unprocessable", http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRejectingServer(t, tc.status)
			bus := events.New()
			s, err := OpenSession(context.Background(), bus, "chat-poison", SessionOptions{
				TokenProvider:   testTokenProvider,
				ClientOptions:   ClientOptions{BaseURL: srv.URL},
				OutboxDir:       t.TempDir(),
				MaxUnflushed:    100,
				CreateTitle:     "Poison Status",
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

			publishTurnStart(bus, "chat-poison", "turn:1", "content the server will always refuse")

			waitUntil(t, "sync to stop on a non-retryable client error", s.Stopped)
			if reason := s.StopReason(); reason == "" {
				t.Error("sync stopped but StopReason() is empty; the contract requires it to say so, and a silent stop is indistinguishable from a healthy idle session")
			}
		})
	}
}
