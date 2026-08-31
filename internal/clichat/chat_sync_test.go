package clichat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func TestAttachCLISyncDisabled(t *testing.T) {
	bus := events.New()
	sess := chat.NewSession(&config.Resolved{}, nil)
	sess.EventBus = bus

	detach := attachCLISync(sess, &config.Resolved{Sync: config.SyncConfig{Enabled: false}})
	if detach == nil {
		t.Fatal("expected non-nil detach func")
	}
	detach()
}

func TestAttachCLISyncEnabled(t *testing.T) {
	var createdCount int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&createdCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: "cli-sync-1", Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: nil})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := events.New()
	res := &config.Resolved{
		Sync: config.SyncConfig{
			Enabled:          true,
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     50,
		},
	}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "cli-sync-1"
	sess.SessionDir = t.TempDir()
	sess.EventBus = bus

	detach := attachCLISync(sess, res)
	time.Sleep(50 * time.Millisecond)
	detach()

	if atomic.LoadInt32(&createdCount) != 1 {
		t.Errorf("createdCount = %d, want 1", atomic.LoadInt32(&createdCount))
	}
}
