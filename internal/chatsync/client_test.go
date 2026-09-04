package chatsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestClientCreateAndGetSession(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		var input CreateSessionParams
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{
			ID:             "sess-new-123",
			OrganizationID: "org-1",
			UserID:         "user-1",
			Title:          input.Title,
			Status:         "running",
		})
	})

	mux.HandleFunc("GET /v1/chat-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{
			ID:             id,
			OrganizationID: "org-1",
			UserID:         "user-1",
			Title:          "Existing Session",
			Status:         "running",
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	ctx := context.Background()

	// 1. CreateSession
	created, err := client.CreateSession(ctx, CreateSessionParams{
		Title: "New Session",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if created.ID != "sess-new-123" || created.Title != "New Session" {
		t.Errorf("created = %+v", created)
	}

	// 2. GetSession
	got, err := client.GetSession(ctx, "sess-new-123")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != "sess-new-123" {
		t.Errorf("got = %+v", got)
	}
}

func TestClientAppendEventsAndNext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Events []EventItem `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items := req.Events
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{
			InsertedCount: len(items),
			LastSeq:       items[len(items)-1].Seq,
		})
	})

	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(NextInput{
			Input: &SessionInput{
				ID:        "inp-1",
				SessionID: r.PathValue("id"),
				Kind:      "message",
				Body:      "hello from web",
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	ctx := context.Background()

	// 1. AppendEvents
	res, err := client.AppendEvents(ctx, "sess-1", []EventItem{
		{Seq: 1, Type: TypeTurnStarted, Payload: json.RawMessage(`{"text":"hi"}`)},
		{Seq: 2, Type: TypeTurnEnded, Payload: json.RawMessage(`{"reason":"completed"}`)},
	})
	if err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	if res.InsertedCount != 2 || res.LastSeq != 2 {
		t.Errorf("AppendResult = %+v, want count=2, lastSeq=2", res)
	}

	// 2. NextInput
	next, err := client.NextInput(ctx, "sess-1", 1)
	if err != nil {
		t.Fatalf("NextInput: %v", err)
	}
	if next.Input == nil || next.Input.Body != "hello from web" {
		t.Errorf("NextInput = %+v", next)
	}
}

func TestClientAppendEventsWithTraceSendsCorrelationHeaders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Mivia-Upload-Batch-ID"); got != "batch-7" {
			t.Errorf("batch header = %q", got)
		}
		if got := r.Header.Get("X-Mivia-Writer-ID"); got != "writer-2" {
			t.Errorf("writer header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{InsertedCount: 1, LastSeq: 9})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	got, err := client.AppendEventsWithTrace(context.Background(), "sess-1", []EventItem{{Seq: 9, Type: TypeTurnEnded}}, "batch-7", "writer-2")
	if err != nil {
		t.Fatalf("AppendEventsWithTrace: %v", err)
	}
	if got.LastSeq != 9 {
		t.Fatalf("LastSeq = %d, want 9", got.LastSeq)
	}
}

func TestClientAuthRetryOn401(t *testing.T) {
	var requestCount int32

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		auth := r.Header.Get("Authorization")
		if count == 1 {
			// First request with stale token: 401
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Second request with refreshed token: 200
		if auth != "Bearer token-fresh" {
			t.Errorf("second request auth = %q, want 'Bearer token-fresh'", auth)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-1", Status: "running"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	var tokenRefreshCount int32
	tokenProvider := func(ctx context.Context, forceRefresh bool) (string, error) {
		if forceRefresh {
			atomic.AddInt32(&tokenRefreshCount, 1)
			return "token-fresh", nil
		}
		return "token-stale", nil
	}

	client, err := NewClient(tokenProvider, ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	sess, getErr := client.GetSession(context.Background(), "sess-1")
	if getErr != nil {
		t.Fatalf("GetSession after retry failed: %v", getErr)
	}
	if sess.ID != "sess-1" {
		t.Errorf("sess = %+v", sess)
	}
	if atomic.LoadInt32(&requestCount) != 2 {
		t.Errorf("requestCount = %d, want 2", atomic.LoadInt32(&requestCount))
	}
	if atomic.LoadInt32(&tokenRefreshCount) != 1 {
		t.Errorf("tokenRefreshCount = %d, want 1", atomic.LoadInt32(&tokenRefreshCount))
	}
}

func TestClient409ConflictTypedError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{
			StatusCode: 409,
			Error:      "Conflict",
			Message:    json.RawMessage(`"session is owned by another writer"`),
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	_, err := client.AppendEvents(context.Background(), "sess-1", []EventItem{{Seq: 1, Type: TypeTurnStarted}})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}

	// Assert typed ErrConflict matching
	if !errors.Is(err, ErrConflict) {
		t.Errorf("errors.Is(err, ErrConflict) = false, got err = %v", err)
	}

	var confErr *ConflictError
	if !errors.As(err, &confErr) {
		t.Fatalf("errors.As(err, &confErr) = false, got %T: %v", err, err)
	}
	if confErr.Message != "session is owned by another writer" || confErr.StatusCode != 409 {
		t.Errorf("confErr = %+v, want message='session is owned by another writer', status=409", confErr)
	}
}

func TestClientAppendEvents_MatchesWireEnvelope(t *testing.T) {
	var receivedBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{InsertedCount: 1, LastSeq: 1})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	_, err := client.AppendEvents(context.Background(), "sess-1", []EventItem{{Seq: 1, Type: TypeTurnStarted}})
	if err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	eventsArr, ok := receivedBody["events"].([]any)
	if !ok || len(eventsArr) != 1 {
		t.Fatalf("expected body with 'events' array, got %v", receivedBody)
	}
}

func TestNewClient_DefaultTimeoutZero(t *testing.T) {
	client := newTestClient(t, ClientOptions{BaseURL: "http://localhost:8080"})
	if client.httpClient == nil {
		t.Fatal("expected httpClient to be non-nil")
	}
	if client.httpClient.Timeout != 0 {
		t.Errorf("default client timeout = %v, want 0 (per-request context deadlines govern)", client.httpClient.Timeout)
	}
}
