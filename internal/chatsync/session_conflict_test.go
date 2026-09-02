package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// poisonServer always answers the append endpoint with a non-sequence 400 -
// the one outcome that still latches sync terminally now that a 409 and a
// 404 recover - and counts the create, heartbeat and poll traffic so a test
// can prove they stopped.
type poisonServer struct {
	creates    atomic.Int32
	heartbeats atomic.Int32
	polls      atomic.Int32
}

func newPoisonServer(t *testing.T) (*poisonServer, *httptest.Server) {
	t.Helper()
	ps := &poisonServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		n := ps.creates.Add(1)
		id := "sess-poison-1"
		if n > 1 {
			id = "sess-poison-forked"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: id, Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{
			StatusCode: 400,
			Error:      "Bad Request",
			Message:    json.RawMessage(`"type must be at most 100 characters"`),
		})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		ps.heartbeats.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		ps.polls.Add(1)
		select {
		case <-time.After(40 * time.Millisecond):
		case <-r.Context().Done():
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return ps, srv
}

// TestSyncSession_PoisonStopsSyncWithoutForking pins that the settled poison
// rule survives recovery: a 400 the server will refuse identically from any
// session must NOT mint a new one. Heartbeat and poller stop with the pusher.
func TestSyncSession_PoisonStopsSyncWithoutForking(t *testing.T) {
	ps, srv := newPoisonServer(t)

	bus := events.New()
	opts := SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		CreateTitle:     "Poison Session",
		HeartbeatPeriod: 20 * time.Millisecond,
		EnablePolling:   true,
		PollWaitSeconds: 1,
	}
	syncSess, err := OpenSession(context.Background(), bus, "sess-poison-1", opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = syncSess.Stop(stopCtx)
	}()

	publishTurnStart(bus, "sess-poison-1", "turn:1", "hello")
	waitUntil(t, "the poison 400 to latch", syncSess.Stopped)

	if got := ps.creates.Load(); got != 1 {
		t.Errorf("CreateSession calls = %d, want 1 (a poison 400 must not fork)", got)
	}
	if got := syncSess.SessionID(); got != "sess-poison-1" {
		t.Errorf("SessionID() = %q, want %q (session must not be replaced)", got, "sess-poison-1")
	}
	hbBefore := ps.heartbeats.Load()
	pollBefore := ps.polls.Load()
	time.Sleep(300 * time.Millisecond)
	if got := ps.heartbeats.Load(); got != hbBefore {
		t.Errorf("heartbeats kept running after the stop: %d -> %d", hbBefore, got)
	}
	if got := ps.polls.Load(); got != pollBefore {
		t.Errorf("poller kept running after the stop: %d -> %d", pollBefore, got)
	}
}

// TestSyncSession_ConflictRecoversOntoANewSession inverts the former
// "409 stops sync without forking" test, per the user's decision that a
// session ended or deleted from the web must never stop a live CLI. The
// backlog moves to a fresh session; heartbeat and poller follow it and keep
// running; the new stream opens with a sync.forked naming both sessions.
func TestSyncSession_ConflictRecoversOntoANewSession(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("conflict-recover")
	bus, s := openAgainstFake(t, f, a, t.TempDir())

	f.EndSession(a)
	publishTurnStart(bus, a, "turn:1", "hello after the end")

	waitUntil(t, "the backlog to land in a new session", func() bool {
		ids := f.SessionIDs()
		return len(ids) == 2 && f.LastSeq(ids[1]) >= 2
	})
	ids := f.SessionIDs()
	b := ids[1]
	if got := s.SessionID(); got != b {
		t.Fatalf("SessionID() = %q, want the replacement %q", got, b)
	}
	if s.Stopped() {
		t.Fatalf("sync stopped (%q); a 409 must recover, not latch", s.StopReason())
	}
	evs := f.Events(b)
	if evs[0].Type != TypeTurnStarted || evs[0].Seq != 1 {
		t.Fatalf("first event in %s = %s seq %d, want the backlog renumbered from 1", b, evs[0].Type, evs[0].Seq)
	}
	marker := forkMarkerIn(t, evs)
	if marker.NewSessionID != b || marker.ForkedFrom != a {
		t.Errorf("marker = %+v, want new_session_id=%s forked_from=%s", marker, b, a)
	}
}

// forkMarkerIn returns the sync.forked marker in a stream, failing the test
// when there is none. The marker follows the renumbered backlog.
func forkMarkerIn(t *testing.T, evs []StoredEvent) SyncForkedPayload {
	t.Helper()
	for _, ev := range evs {
		if ev.Type != TypeSyncForked {
			continue
		}
		var marker SyncForkedPayload
		if err := json.Unmarshal(ev.Payload, &marker); err != nil {
			t.Fatal(err)
		}
		return marker
	}
	t.Fatalf("no sync.forked marker in a stream of %d events", len(evs))
	return SyncForkedPayload{}
}
