package clichat

import (
	"encoding/json"
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
)

func TestAttachCLISyncDisabled(t *testing.T) {
	bus := events.New()
	sess := chat.NewSession(&config.Resolved{}, nil)
	sess.EventBus = bus

	detach := attachCLISync(sess, t.TempDir(), &config.Resolved{Sync: config.ResolvedSync{Disabled: true}})
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
	sess.EventBus = bus

	detach := attachCLISync(sess, t.TempDir(), res)
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
	sess.EventBus = events.New()

	installTestAuthToken(t)
	detach := attachCLISync(sess, t.TempDir(), res)
	time.Sleep(50 * time.Millisecond)
	detach()

	if n := atomic.LoadInt32(&unauthenticated); n != 0 {
		t.Errorf("server saw %d unauthenticated request(s), want 0", n)
	}
	if n := atomic.LoadInt32(&created); n != 1 {
		t.Errorf("created = %d, want 1 (an authenticated create must reach the server)", n)
	}
}

// TestAttachCLISyncDetach_DeliversTheFullBurstBeforeStopping reproduces the
// tail-loss bug found dogfooding [sync].stream_assistant = true: a one-shot
// turn's deltas are Published to sess.EventBus and detach() (oneShot's defer)
// runs on the very next line, with nothing in between to guarantee chatsync's
// bus subscription has actually delivered that whole burst to HandleEvent
// yet. Publish() only enqueues onto the subscription's own bounded queue and
// returns immediately (internal/events.Bus doc: "Publish never blocks on a
// handler") - Stop()'s drainAndFlushFinal only drains what already reached
// SyncSession's own eventCh, so without a Flush first, the tail of a
// heavy-volume turn can still be sitting undelivered in the bus queue when
// the process exits. This asserts every one of a 220-event burst (well under
// the bus's 256-capacity queue, so no capacity-based drop-oldest muddies the
// result) reaches the server.
func TestAttachCLISyncDetach_DeliversTheFullBurstBeforeStopping(t *testing.T) {
	var mu sync.Mutex
	received := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: "cli-burst-1", Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: nil})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []json.RawMessage `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		received += len(body.Events)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.AppendResult{LastSeq: int64(received), InsertedCount: len(body.Events)})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := events.New()
	res := &config.Resolved{
		Sync: config.ResolvedSync{
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     500,
			StreamAssistant:  true,
		},
	}
	installTestAuthToken(t)

	sess := chat.NewSession(res, nil)
	sess.SessionID = "cli-burst-1"
	sess.EventBus = bus

	detach := attachCLISync(sess, t.TempDir(), res)

	const turnID = "turn:1"
	bus.Publish(events.Event{Kind: events.KindTurnStart, SessionID: sess.SessionID, TurnID: turnID, Detail: "hi"})
	const deltaCount = 220
	for i := 0; i < deltaCount; i++ {
		bus.Publish(events.Event{
			Kind: events.KindAssistant, SessionID: sess.SessionID, TurnID: turnID,
			Detail: "delta", Content: "x",
		})
	}
	// The final non-delta assistant event and turn end, exactly as the real
	// turn-completion path emits them once streaming is done.
	bus.Publish(events.Event{Kind: events.KindAssistant, SessionID: sess.SessionID, TurnID: turnID, Content: "final"})
	bus.Publish(events.Event{Kind: events.KindTurnEnd, SessionID: sess.SessionID, TurnID: turnID, Detail: "completed"})
	// No sleep, no explicit Flush here: this is exactly oneShot's real shape
	// (defer attachCLISync(...)() fires on the next line after the turn's
	// last Publish call returns), and it must not lose events on its own.
	detach()

	mu.Lock()
	got := received
	mu.Unlock()
	// turn.started (1) + deltaCount deltas + final assistant.message marker
	// (fragments>0, empty text - projectAssistant's INV-1 terminator) + turn.ended.
	want := 1 + deltaCount + 1 + 1
	if got != want {
		t.Errorf("server received %d events, want %d (the burst's tail was lost between Publish and Stop)", got, want)
	}
}

// TestAttachCLISyncDetach_SurvivesASlowFinalFlush reproduces the second half
// of the same live-dogfooding failure: even with the burst fully delivered to
// chatsync's HandleEvent (the fix above), a REAL network round trip for the
// final flush can take longer than a short ctx budget - FlushOutbox sends
// the whole unflushed backlog as ONE request, uncapped, and a real remote
// server plus a genuinely accumulated backlog (periodic mid-turn flushes
// cannot always keep pace with local event generation once
// [sync].stream_assistant multiplies event count) can legitimately take
// several seconds. A ctx that expires first makes Stop return early via its
// timedOut path, and the caller's own process exit right after kills the
// goroutine Stop handed the rest of the work to - so the backlog is not
// merely delayed, it is permanently lost (nothing re-opens a one-shot run's
// outbox directory later). This pins detach()'s ctx budget
// (chatsync.RecommendedStopTimeout) as long enough for the /events endpoint
// to take 3 real seconds to respond.
func TestAttachCLISyncDetach_SurvivesASlowFinalFlush(t *testing.T) {
	var mu sync.Mutex
	received := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: "cli-slow-1", Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: nil})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		// The real-network stand-in: this is what makes 2s fail and 15s pass -
		// not the payload, not retries, just wall-clock time on one request.
		time.Sleep(3 * time.Second)
		var body struct {
			Events []json.RawMessage `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		received += len(body.Events)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.AppendResult{LastSeq: int64(received), InsertedCount: len(body.Events)})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bus := events.New()
	res := &config.Resolved{
		Sync: config.ResolvedSync{
			APIURL: srv.URL, PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 500,
		},
	}
	installTestAuthToken(t)

	sess := chat.NewSession(res, nil)
	sess.SessionID = "cli-slow-1"
	sess.EventBus = bus

	detach := attachCLISync(sess, t.TempDir(), res)
	bus.Publish(events.Event{Kind: events.KindTurnStart, SessionID: sess.SessionID, TurnID: "turn:1", Detail: "hi"})
	bus.Publish(events.Event{Kind: events.KindTurnEnd, SessionID: sess.SessionID, TurnID: "turn:1", Detail: "completed"})
	detach()

	mu.Lock()
	got := received
	mu.Unlock()
	if got != 2 {
		t.Errorf("server received %d events, want 2 (the final flush did not get enough time to complete a slow 3s request)", got)
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
