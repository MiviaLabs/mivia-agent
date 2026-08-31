package uiadapter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

func setupSyncMockServer(mu *sync.Mutex, createdIDs *[]string, sessionEvents map[string][]chatsync.EventItem) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		var params chatsync.CreateSessionParams
		_ = json.NewDecoder(r.Body).Decode(&params)
		mu.Lock()
		sessID := fmt.Sprintf("remote-%d", len(*createdIDs)+1)
		*createdIDs = append(*createdIDs, sessID)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: sessID, Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req struct {
			Events []chatsync.EventItem `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		items := req.Events
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

	installTestAuthToken(t)

	bus := events.New()
	tmpDir := t.TempDir()
	res := &config.Resolved{
		Model: "test-model",
		Sync: config.ResolvedSync{
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

// installTestAuthToken points HOME at a temp dir holding a valid, unexpired
// CLI session, so the sync wiring resolves a real token provider without a
// network round trip.
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

// TestSessionPool_SyncAuthenticatesEveryRequest drives the TUI pool wiring
// against a server that answers 401 to any request without a bearer token. It
// fails if a pooled session ever uploads conversation content anonymously.
func TestSessionPool_SyncAuthenticatesEveryRequest(t *testing.T) {
	var mu sync.Mutex
	var createdIDs []string
	sessionEvents := make(map[string][]chatsync.EventItem)
	inner := setupSyncMockServer(&mu, &createdIDs, sessionEvents)
	defer inner.Close()

	var unauthenticated int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			atomic.AddInt32(&unauthenticated, 1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	installTestAuthToken(t)

	res := &config.Resolved{
		Model: "test-model",
		Sync: config.ResolvedSync{
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     100,
		},
	}
	sess1 := chat.NewSession(res, nil)
	sess1.SessionID = "local-auth-1"
	sess1.SessionDir = t.TempDir()
	sess1.EventBus = events.New()

	pool := uiadapter.NewSessionPool(sess1, res, nil, false)
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.ReleaseLeases(ctx)

	if n := atomic.LoadInt32(&unauthenticated); n != 0 {
		t.Errorf("server saw %d unauthenticated request(s), want 0", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(createdIDs) != 1 {
		t.Errorf("createdIDs = %v, want exactly 1 authenticated create", createdIDs)
	}
}

// TestSessionPool_DoesNotExecuteRemoteInput pins that server-supplied text
// never becomes a local turn. The remote-input path fed conv.Send directly,
// with no confirmation, into a runtime whose approval default auto-approves
// run_command, so a compromised or hostile API could run commands on the
// user's machine. The poller stays in internal/chatsync for the S9 redesign,
// but nothing may reach it from here.
//
// The observable is the wire: a session pool that never polls has no remote
// input to execute. The server offers one on every poll and records every
// consume, so a live poller cannot hide.
func TestSessionPool_DoesNotExecuteRemoteInput(t *testing.T) {
	var polls, consumes int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: "remote-input-1", Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.AppendResult{InsertedCount: 1, LastSeq: 1})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&polls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{
			Input: &chatsync.SessionInput{ID: "input-1", Body: "rm -rf /"},
		})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&consumes, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.SessionInput{ID: r.PathValue("inputID"), Body: "rm -rf /"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	installTestAuthToken(t)

	res := &config.Resolved{
		Model: "test-model",
		Sync: config.ResolvedSync{
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     100,
		},
	}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "local-remote-input"
	sess.SessionDir = t.TempDir()
	sess.EventBus = events.New()

	pool := uiadapter.NewSessionPool(sess, res, nil, false)
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.ReleaseLeases(ctx)

	if n := atomic.LoadInt32(&polls); n != 0 {
		t.Errorf("inputs/next polls = %d, want 0; the remote-input path must be unreachable", n)
	}
	if n := atomic.LoadInt32(&consumes); n != 0 {
		t.Errorf("inputs consume calls = %d, want 0; a consumed input is one the CLI committed to running", n)
	}
}
