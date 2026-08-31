package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func setupE2EMockServer(mu *sync.Mutex, eventsList *[]EventItem) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-e2e-1", Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Events []EventItem `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		items := req.Events
		mu.Lock()
		*eventsList = append(*eventsList, items...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{InsertedCount: len(items), LastSeq: items[len(items)-1].Seq})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
	})
	return httptest.NewServer(mux)
}

func TestSyncSessionEndToEndFlow(t *testing.T) {
	var mu sync.Mutex
	var appendedEvents []EventItem

	srv := setupE2EMockServer(&mu, &appendedEvents)
	defer srv.Close()

	bus := events.New()
	outboxDir := filepath.Join(t.TempDir(), "outbox-e2e")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := SessionOptions{
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       outboxDir,
		CreateTitle:     "E2E Session",
		HeartbeatPeriod: 50 * time.Millisecond,
		EnablePolling:   true,
		PollWaitSeconds: 1,
	}

	syncSess, err := OpenSession(ctx, bus, "", opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	publishTestTurn(bus, "sess-e2e-1", "turn:1")
	time.Sleep(50 * time.Millisecond)

	if err := syncSess.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	verifyAppendedEvents(t, &mu, appendedEvents)
}

func publishTestTurn(bus *events.Bus, sessionID, turnID string) {
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: sessionID,
		TurnID:    turnID,
		Detail:    "user message",
		Timestamp: time.Now(),
	})
	bus.Publish(events.Event{
		Kind:      events.KindAssistant,
		SessionID: sessionID,
		TurnID:    turnID,
		Content:   "assistant response",
		Timestamp: time.Now(),
	})
	bus.Publish(events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: sessionID,
		TurnID:    turnID,
		Detail:    "completed",
		Timestamp: time.Now(),
	})
}

func verifyAppendedEvents(t *testing.T, mu *sync.Mutex, appendedEvents []EventItem) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if len(appendedEvents) != 3 {
		t.Fatalf("appendedEvents count = %d, want 3", len(appendedEvents))
	}
	if appendedEvents[0].Type != TypeTurnStarted || appendedEvents[1].Type != TypeAssistantMessage || appendedEvents[2].Type != TypeTurnEnded {
		t.Errorf("appended types: %v, %v, %v", appendedEvents[0].Type, appendedEvents[1].Type, appendedEvents[2].Type)
	}
	if appendedEvents[0].Seq != 1 || appendedEvents[1].Seq != 2 || appendedEvents[2].Seq != 3 {
		t.Errorf("seqs = %d, %d, %d; want 1, 2, 3", appendedEvents[0].Seq, appendedEvents[1].Seq, appendedEvents[2].Seq)
	}
}

func setupForkMockServer(createCount *int32, mu *sync.Mutex, sessionEvents map[string][]EventItem) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(createCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		sessID := "sess-fork-1"
		if count > 1 {
			sessID = "sess-fork-2"
		}
		_ = json.NewEncoder(w).Encode(Session{ID: sessID, Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "sess-fork-1" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(ErrorEnvelope{
				StatusCode: 409,
				Error:      "Conflict",
				Message:    json.RawMessage(`"writer conflict"`),
			})
			return
		}
		var req struct {
			Events []EventItem `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		items := req.Events
		mu.Lock()
		sessionEvents[id] = append(sessionEvents[id], items...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{InsertedCount: len(items), LastSeq: items[len(items)-1].Seq})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	return httptest.NewServer(mux)
}

func TestSyncSessionHandlesForkOn409(t *testing.T) {
	var createCount int32
	var mu sync.Mutex
	sessionEvents := make(map[string][]EventItem)

	srv := setupForkMockServer(&createCount, &mu, sessionEvents)
	defer srv.Close()

	bus := events.New()
	outboxDir := filepath.Join(t.TempDir(), "outbox-fork")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := SessionOptions{
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       outboxDir,
		CreateTitle:     "Fork Session",
		HeartbeatPeriod: 50 * time.Millisecond,
	}

	syncSess, err := OpenSession(ctx, bus, "", opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-fork-1",
		TurnID:    "turn:1",
		Detail:    "test conflict",
		Timestamp: time.Now(),
	})
	time.Sleep(100 * time.Millisecond)
	_ = syncSess.Stop(ctx)

	verifyForkedSession(t, atomic.LoadInt32(&createCount), &mu, sessionEvents)
}

func verifyForkedSession(t *testing.T, count int32, mu *sync.Mutex, sessionEvents map[string][]EventItem) {
	t.Helper()
	if count < 2 {
		t.Errorf("createCount = %d, want at least 2 sessions created on fork", count)
	}

	mu.Lock()
	defer mu.Unlock()
	forkedEvents := sessionEvents["sess-fork-2"]
	if len(forkedEvents) == 0 {
		t.Fatal("no events received on forked session sess-fork-2")
	}

	foundForkEvent := false
	for i, ev := range forkedEvents {
		if ev.Seq != int64(i+1) {
			t.Errorf("forked event[%d].Seq = %d, want %d", i, ev.Seq, i+1)
		}
		if ev.Type == TypeSyncForked {
			foundForkEvent = true
		}
	}
	if !foundForkEvent {
		t.Errorf("sync.forked event not found in forked session events: %+v", forkedEvents)
	}
}

func TestSyncSession_DistinctLocalAndRemoteSessionIDs(t *testing.T) {
	var mu sync.Mutex
	var receivedEvents []EventItem

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: "remote-server-uuid-999", Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Events []EventItem `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		receivedEvents = append(receivedEvents, req.Events...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{InsertedCount: len(req.Events), LastSeq: int64(len(receivedEvents))})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := events.New()
	opts := SessionOptions{
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		CreateTitle:     "Distinct IDs",
		HeartbeatPeriod: 1 * time.Second,
	}

	// OpenSession with local session ID "local-client-1"
	syncSess, err := OpenSession(context.Background(), bus, "local-client-1", opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	// Publish event with SessionID = "local-client-1"
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "local-client-1",
		TurnID:    "turn:1",
		Detail:    "hello world",
		Timestamp: time.Now(),
	})

	time.Sleep(50 * time.Millisecond)
	_ = syncSess.Stop(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(receivedEvents) == 0 {
		t.Fatal("expected projected events to be received on remote server, got 0")
	}
}

func TestSyncSession_ResumeWithUnflushedEventsPreservesMonotonicity(t *testing.T) {
	outboxDir := t.TempDir()

	// Pre-populate outbox with events up to seq 5, cursor at 2
	ob, err := OpenOutbox(outboxDir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	_ = ob.Append(
		WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Envelope: Envelope{Turn: "turn:1"}}},
		WireEvent{Seq: 2, Type: TypeTurnEnded, Payload: &TurnEndedPayload{Envelope: Envelope{Turn: "turn:1"}}},
		WireEvent{Seq: 3, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Envelope: Envelope{Turn: "turn:2"}}},
		WireEvent{Seq: 4, Type: TypeTurnEnded, Payload: &TurnEndedPayload{Envelope: Envelope{Turn: "turn:2"}}},
		WireEvent{Seq: 5, Type: TypeTurnStarted, Payload: &TurnStartedPayload{Envelope: Envelope{Turn: "turn:3"}}},
	)
	_ = ob.AdvanceCursor(2)
	_ = ob.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-resumed", Status: "running", LastSeq: 2})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Events []EventItem `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{InsertedCount: len(req.Events), LastSeq: req.Events[len(req.Events)-1].Seq})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := events.New()
	opts := SessionOptions{
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       outboxDir,
		CreateTitle:     "Resume Test",
		HeartbeatPeriod: 1 * time.Second,
	}

	syncSess, err := OpenSession(context.Background(), bus, "sess-resumed", opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	// Publish new turn
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-resumed",
		TurnID:    "turn:4",
		Detail:    "start turn 4",
		Timestamp: time.Now(),
	})

	time.Sleep(100 * time.Millisecond)
	_ = syncSess.Stop(context.Background())

	// Re-open outbox and check that new event received Seq >= 6
	obCheck, err := OpenOutbox(outboxDir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox check: %v", err)
	}
	defer obCheck.Close()
	if obCheck.MaxSeq() < 6 {
		t.Errorf("obCheck.MaxSeq() = %d, want >= 6", obCheck.MaxSeq())
	}
}

func TestHandleEvent_OutboxOverflowDoesNotAdvanceSeq(t *testing.T) {
	outboxDir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-overflow", Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := events.New()
	opts := SessionOptions{
		ClientOptions: ClientOptions{BaseURL: srv.URL},
		OutboxDir:     outboxDir,
		MaxUnflushed:  1, // Capacity is exactly 1 event
		CreateTitle:   "Overflow Test",
	}

	syncSess, err := OpenSession(context.Background(), bus, "sess-overflow", opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	// 1. Publish 1st event -> successfully appended (Seq: 1)
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-overflow",
		TurnID:    "turn:1",
		Detail:    "message 1",
		Timestamp: time.Now(),
	})
	time.Sleep(50 * time.Millisecond)

	if syncSess.LastSeq() != 1 {
		t.Fatalf("syncSess.LastSeq() = %d, want 1", syncSess.LastSeq())
	}

	// 2. Publish 2nd event -> exceeds capacity (MaxUnflushed=1, unflushed=1).
	// Must NOT consume sequence number 2!
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-overflow",
		TurnID:    "turn:2",
		Detail:    "message 2 (overflowed)",
		Timestamp: time.Now(),
	})
	time.Sleep(50 * time.Millisecond)

	if syncSess.LastSeq() != 1 {
		t.Errorf("syncSess.LastSeq() after overflow = %d, want 1 (must not consume sequence on overflow)", syncSess.LastSeq())
	}

	_ = syncSess.Stop(context.Background())
}
