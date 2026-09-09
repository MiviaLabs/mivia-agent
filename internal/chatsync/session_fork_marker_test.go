package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestOpenSession_ForkMarkerAppendFailureIsNotSwallowed covers the third seq
// rollback site. AttachSession forks when a foreign writer owns the session;
// the sync.forked marker that follows consumes a seq, so an append the outbox
// refuses must fail loudly instead of leaving the counter ahead of the file.
// Under the deferred attach the fork happens on the FIRST message, so the
// failure surfaces as that attach's terminal stop - named through the same
// channel every other fatal condition uses - rather than an OpenSession
// error: the open itself is local and cannot fail on a fork.
func TestOpenSession_ForkMarkerAppendFailureIsNotSwallowed(t *testing.T) {
	outboxDir := t.TempDir()

	// Two unflushed events fill the outbox to its cap exactly, so the fork
	// marker cannot be stored.
	ob, err := OpenOutbox(outboxDir, 2)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	if err := ob.Append(
		WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Envelope: Envelope{Turn: "turn:1"}}},
		WireEvent{Seq: 2, Type: TypeTurnEnded, Payload: &TurnEndedPayload{Envelope: Envelope{Turn: "turn:1"}}},
	); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	if err := ob.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		// Server is ahead of the local cursor (0), which sends AttachSession
		// to the writer check.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-foreign", Status: "running", LastSeq: 3})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		// A different writer owns the tail -> FORK.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]StoredEvent{{
			Seq:     1,
			Type:    TypeTurnStarted,
			Payload: json.RawMessage(`{"writer_id":"somebody-else"}`),
		}})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/end", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "ended"})
	})
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-forked-new", Status: "running", LastSeq: 0})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-foreign", SessionOptions{
		TokenProvider:    testTokenProvider,
		ClientOptions:    ClientOptions{BaseURL: srv.URL},
		ProjectorOptions: ProjectorOptions{WriterID: "me"},
		RemoteSessionID:  "sess-foreign",
		OutboxDir:        outboxDir,
		MaxUnflushed:     2,
		CreateTitle:      "Fork Marker",
		HeartbeatPeriod:  10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v; the open is local and must not touch the fork", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = syncSess.Stop(stopCtx)
	}()

	publishTurnStart(bus, "sess-foreign", "turn:1", "the message that attaches")
	waitUntil(t, "the failed fork marker to latch the terminal stop", syncSess.Stopped)
	if reason := syncSess.StopReason(); !strings.Contains(reason, "append fork marker") {
		t.Errorf("StopReason = %q, want it to name the failed fork marker append", reason)
	}
}
