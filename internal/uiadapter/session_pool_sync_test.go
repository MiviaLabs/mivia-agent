package uiadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

func setupSyncMockServer(mu *sync.Mutex, createdIDs *[]string, sessionEvents map[string][]chatsync.EventItem) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		var params chatsync.CreateSessionParams
		_ = json.NewDecoder(r.Body).Decode(&params)
		mu.Lock()
		sessID := "remote-" + params.Title
		*createdIDs = append(*createdIDs, sessID)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: sessID, Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var items []chatsync.EventItem
		_ = json.NewDecoder(r.Body).Decode(&items)
		mu.Lock()
		sessionEvents[id] = append(sessionEvents[id], items...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.AppendResult{InsertedCount: len(items), LastSeq: int64(len(items))})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: nil})
	})
	return httptest.NewServer(mux)
}

func TestSessionPool_SyncPerPooledSession(t *testing.T) {
	var mu sync.Mutex
	var createdIDs []string
	sessionEvents := make(map[string][]chatsync.EventItem)

	srv := setupSyncMockServer(&mu, &createdIDs, sessionEvents)
	defer srv.Close()

	bus := events.New()
	tmpDir := t.TempDir()
	res := &config.Resolved{
		Model: "test-model",
		Sync: config.SyncConfig{
			Enabled:          true,
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     100,
		},
	}

	sess1 := chat.NewSession(res, nil)
	sess1.SessionID = "local-1"
	sess1.SessionDir = tmpDir
	sess1.EventBus = bus

	pool := uiadapter.NewSessionPool(sess1, res, nil, false)

	conv2, err := pool.CreateFresh()
	if err != nil || conv2 == nil {
		t.Fatalf("CreateFresh: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.ReleaseLeases(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(createdIDs) < 2 {
		t.Errorf("createdIDs = %v, want at least 2 remote sessions created", createdIDs)
	}
}
