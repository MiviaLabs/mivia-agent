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

// TestSessionPool_ReleaseLeasesFlushesBusBeforeStopping reproduces the same
// tail-loss shape TestAttachCLISyncDetach_DeliversTheFullBurstBeforeStopping
// pins for the plain-CLI surface (DC-30,
// .agents/quality/defect-taxonomy.md), for the TUI's pooled-session teardown
// path instead: a burst of events lands on sess1's bus right before the TUI
// quits and calls ReleaseLeases, with no sleep and no explicit Flush from the
// caller - exactly how a subagent fan-out or a [sync].stream_assistant = true
// turn's tail looks at process exit. ReleaseLeases must flush THIS session's
// own bus before stopping its sync session, or the burst is silently lost.
func TestSessionPool_ReleaseLeasesFlushesBusBeforeStopping(t *testing.T) {
	var mu sync.Mutex
	var createdIDs []string
	sessionEvents := make(map[string][]chatsync.EventItem)
	srv := setupSyncMockServer(&mu, &createdIDs, sessionEvents)
	defer srv.Close()

	installTestAuthToken(t)

	bus := events.New()
	res := &config.Resolved{
		Model: "test-model",
		Sync: config.ResolvedSync{
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     500,
			StreamAssistant:  true,
		},
	}
	sess1 := chat.NewSession(res, nil)
	sess1.SessionID = "local-burst-1"
	sess1.SessionDir = t.TempDir()
	sess1.EventBus = bus

	pool := uiadapter.NewSessionPool(sess1, res, nil, false)
	time.Sleep(50 * time.Millisecond) // let the initial session create land

	const turnID = "turn:1"
	bus.Publish(events.Event{Kind: events.KindTurnStart, SessionID: sess1.SessionID, TurnID: turnID, Detail: "hi"})
	const deltaCount = 220
	for i := 0; i < deltaCount; i++ {
		bus.Publish(events.Event{
			Kind: events.KindAssistant, SessionID: sess1.SessionID, TurnID: turnID,
			Detail: "delta", Content: "x",
		})
	}
	bus.Publish(events.Event{Kind: events.KindAssistant, SessionID: sess1.SessionID, TurnID: turnID, Content: "final"})
	bus.Publish(events.Event{Kind: events.KindTurnEnd, SessionID: sess1.SessionID, TurnID: turnID, Detail: "completed"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.ReleaseLeases(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(createdIDs) < 1 {
		t.Fatalf("createdIDs = %v, want at least 1 remote session created", createdIDs)
	}
	got := len(sessionEvents[createdIDs[0]])
	want := 1 + deltaCount + 1 + 1
	if got != want {
		t.Errorf("server received %d events for %s, want %d (the burst's tail was lost between Publish and ReleaseLeases)", got, createdIDs[0], want)
	}
}

// TestSessionPool_DoesNotExecuteRemoteInput's assertion (a session pool that
// never polls the inputs/next endpoint) is now the WRONG invariant to pin:
// remote-input polling is intentionally enabled by poolSyncOptions (see
// session_pool.go). The safety property it protected - a hostile or
// compromised API cannot run commands on the user's machine - is now pinned
// in session_pool_remote_input_test.go as
// TestSessionPool_RemoteInputsRejectsUnverifiedAuthor, which proves the
// actual boundary: SessionPool never forwards an input onto RemoteInputs()
// unless its author matches the CLI's own verified principal, and never
// calls conv.Send for one at all (see internal/uiadapter/remote_input.go's
// doc comment on RemoteInputs).
