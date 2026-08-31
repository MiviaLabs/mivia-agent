package clichat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

func TestAttachCLISyncDisabled(t *testing.T) {
	bus := events.New()
	sess := chat.NewSession(&config.Resolved{}, nil)
	sess.EventBus = bus

	detach := attachCLISync(sess, &config.Resolved{Sync: config.ResolvedSync{Disabled: true}})
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
		Sync: config.ResolvedSync{
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     50,
		},
	}
	installTestAuthToken(t)

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

// TestAttachCLISyncAuthenticatesEveryRequest drives the plain-CLI wiring
// against a server that answers 401 to any request without a bearer token,
// the way /v1/chat-sessions does. It fails if the CLI ever uploads
// conversation content anonymously.
func TestAttachCLISyncAuthenticatesEveryRequest(t *testing.T) {
	var created, unauthenticated int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&created, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: "cli-auth-1", Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			atomic.AddInt32(&unauthenticated, 1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()

	res := &config.Resolved{
		Sync: config.ResolvedSync{
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     50,
		},
	}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "cli-auth-1"
	sess.SessionDir = t.TempDir()
	sess.EventBus = events.New()

	installTestAuthToken(t)
	detach := attachCLISync(sess, res)
	time.Sleep(50 * time.Millisecond)
	detach()

	if n := atomic.LoadInt32(&unauthenticated); n != 0 {
		t.Errorf("server saw %d unauthenticated request(s), want 0", n)
	}
	if n := atomic.LoadInt32(&created); n != 1 {
		t.Errorf("created = %d, want 1 (an authenticated create must reach the server)", n)
	}
}

// installTestAuthToken points HOME at a temp dir holding a valid, unexpired
// CLI session, so the sync wiring resolves a real token provider without a
// network round trip. Tests that omit it exercise the logged-out path.
func installTestAuthToken(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := miviaauth.Save(config.UserAuthPath(), miviaauth.Token{
		Bearer:       "test-bearer",
		RefreshToken: "test-refresh",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("save test token: %v", err)
	}
}
