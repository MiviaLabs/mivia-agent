package chatsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestAttachSessionExisting(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{
			ID:      "sess-existing-1",
			LastSeq: 5,
			Status:  "running",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	outboxDir := filepath.Join(t.TempDir(), "outbox")
	ob, _ := OpenOutbox(outboxDir, 100)
	defer ob.Close()
	_ = ob.AdvanceCursor(2)

	att, err := AttachSession(context.Background(), client, ob, CreateSessionParams{}, "sess-existing-1", "")
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	if att.SessionID != "sess-existing-1" || att.ServerSeq != 5 || att.FlushedSeq != 2 {
		t.Errorf("att = %+v, want SessionID=sess-existing-1, ServerSeq=5, FlushedSeq=2", att)
	}
}

func TestAttachSession_ServerAhead_AdoptWhenMine(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{
			ID:      "sess-mine-1",
			LastSeq: 5,
			Status:  "running",
		})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]StoredEvent{
			{Seq: 3, Payload: json.RawMessage(`{"writer_id":"writer-me"}`)},
			{Seq: 4, Payload: json.RawMessage(`{"writer_id":"writer-me"}`)},
			{Seq: 5, Payload: json.RawMessage(`{"writer_id":"writer-me"}`)},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	outboxDir := filepath.Join(t.TempDir(), "outbox")
	ob, _ := OpenOutbox(outboxDir, 100)
	defer ob.Close()
	_ = ob.AdvanceCursor(2)

	att, err := AttachSession(context.Background(), client, ob, CreateSessionParams{}, "sess-mine-1", "writer-me")
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	if att.SessionID != "sess-mine-1" || att.ServerSeq != 5 || att.ForkedFrom != "" {
		t.Errorf("att = %+v, want SessionID=sess-mine-1, ServerSeq=5, ForkedFrom=''", att)
	}
	if ob.Cursor().FlushedSeq != 5 {
		t.Errorf("ob.Cursor().FlushedSeq = %d, want 5", ob.Cursor().FlushedSeq)
	}
}

func TestAttachSession_ServerAhead_ForkWhenForeign(t *testing.T) {
	var endedCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{
			ID:      "sess-foreign-1",
			LastSeq: 5,
			Status:  "running",
		})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]StoredEvent{
			{Seq: 3, Payload: json.RawMessage(`{"writer_id":"writer-foreign"}`)},
			{Seq: 4, Payload: json.RawMessage(`{"writer_id":"writer-foreign"}`)},
		})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/end", func(w http.ResponseWriter, r *http.Request) {
		endedCalled = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-foreign-1", Status: "ended"})
	})
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{
			ID:      "sess-forked-new",
			LastSeq: 0,
			Status:  "running",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	outboxDir := filepath.Join(t.TempDir(), "outbox")
	ob, _ := OpenOutbox(outboxDir, 100)
	defer ob.Close()
	_ = ob.AdvanceCursor(2)

	att, err := AttachSession(context.Background(), client, ob, CreateSessionParams{Title: "Forked"}, "sess-foreign-1", "writer-me")
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	if !endedCalled {
		t.Errorf("expected POST /end on foreign session")
	}
	if att.SessionID != "sess-forked-new" || att.ForkedFrom != "sess-foreign-1" || att.ServerSeq != 0 {
		t.Errorf("att = %+v, want SessionID=sess-forked-new, ForkedFrom=sess-foreign-1, ServerSeq=0", att)
	}
}

func TestAttachSessionNew(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{
			ID:      "sess-new-456",
			LastSeq: 0,
			Status:  "running",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	outboxDir := filepath.Join(t.TempDir(), "outbox")
	ob, _ := OpenOutbox(outboxDir, 100)
	defer ob.Close()

	att, err := AttachSession(context.Background(), client, ob, CreateSessionParams{Title: "Fresh"}, "", "")
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	if att.SessionID != "sess-new-456" || att.ServerSeq != 0 {
		t.Errorf("att = %+v, want SessionID=sess-new-456, ServerSeq=0", att)
	}
}

func TestFlushOutboxAdvancesCursorOnAck(t *testing.T) {
	var appendReceived int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Events []EventItem `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		items := req.Events
		appendReceived = len(items)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{
			InsertedCount: len(items),
			LastSeq:       items[len(items)-1].Seq,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	outboxDir := filepath.Join(t.TempDir(), "outbox")
	ob, _ := OpenOutbox(outboxDir, 100)
	defer ob.Close()

	_ = ob.Append(
		WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "1"}},
		WireEvent{Seq: 2, Type: TypeAssistantMessage, Payload: &AssistantMessagePayload{Text: "2"}},
		WireEvent{Seq: 3, Type: TypeTurnEnded, Payload: &TurnEndedPayload{Reason: "completed"}},
	)

	n, err := FlushOutbox(context.Background(), client, ob, "sess-1")
	if err != nil {
		t.Fatalf("FlushOutbox: %v", err)
	}
	if n != 3 || appendReceived != 3 {
		t.Errorf("n = %d, appendReceived = %d; want 3", n, appendReceived)
	}

	// Verify outbox cursor is now 3 and no unflushed events remain
	if ob.Cursor().FlushedSeq != 3 {
		t.Errorf("cursor = %d, want 3", ob.Cursor().FlushedSeq)
	}
	unflushed, _ := ob.UnflushedEvents()
	if len(unflushed) != 0 {
		t.Errorf("unflushed remaining = %d, want 0", len(unflushed))
	}
}

func TestFlushOutboxPropagatesConflict(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{
			StatusCode: 409,
			Error:      "Conflict",
			Message:    json.RawMessage(`"conflict"`),
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	outboxDir := filepath.Join(t.TempDir(), "outbox")
	ob, _ := OpenOutbox(outboxDir, 100)
	defer ob.Close()

	_ = ob.Append(WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Text: "1"}})

	_, err := FlushOutbox(context.Background(), client, ob, "sess-1")
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict, got %v", err)
	}

	// Cursor must not have advanced on conflict error
	if ob.Cursor().FlushedSeq != 0 {
		t.Errorf("cursor = %d, want 0", ob.Cursor().FlushedSeq)
	}
}
