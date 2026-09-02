package clichat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// lockedBuffer is the notice sink. OnStop runs on a detached goroutine, so
// the test reads what the wiring wrote under the same lock the wiring holds.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureSyncNotices(t *testing.T) *lockedBuffer {
	t.Helper()
	sink := &lockedBuffer{}
	prev := syncNoticeWriter
	syncNoticeWriter = sink
	t.Cleanup(func() { syncNoticeWriter = prev })
	return sink
}

// TestAttachCLISyncSaysItIsRunning gives the user the one thing a background
// uploader owes them: a sign that it is on. Without it, "is my session on the
// tablet?" has no answer short of opening the tablet.
func TestAttachCLISyncSaysItIsRunning(t *testing.T) {
	srv, _ := syncActivationServer(t)
	sink := captureSyncNotices(t)
	res := &config.Resolved{Sync: config.ResolvedSync{APIURL: srv.URL, PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 50}}
	installTestAuthToken(t)
	sess, wsRoot := activationSession(t, res)

	detach := attachCLISync(sess, wsRoot, res)
	time.Sleep(50 * time.Millisecond)
	detach()

	got := sink.String()
	if !strings.Contains(got, "sync") {
		t.Errorf("notices = %q, want a line saying sync is running", got)
	}
	// The endpoint is named so an upload to the wrong backend is visible at
	// the moment it starts; the URL alone is not enough, the source says
	// which file to fix.
	if !strings.Contains(got, srv.URL) || !strings.Contains(got, "[sync] api_url") {
		t.Errorf("notices = %q, want the running line to name %s and its source", got, srv.URL)
	}
}

// TestAttachCLISyncSaysWhyItStopped is the "stop syncing and SAY SO" half.
// A 409 ends the remote session for good; a user who is told nothing keeps
// believing their tablet is following along.
func TestAttachCLISyncSaysWhyItStopped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: "cli-stop-1", Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(chatsync.ErrorEnvelope{
			StatusCode: 409,
			Error:      "Conflict",
			Message:    json.RawMessage(`"session already ended"`),
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sink := captureSyncNotices(t)
	res := &config.Resolved{Sync: config.ResolvedSync{APIURL: srv.URL, PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 50}}
	installTestAuthToken(t)
	sess, wsRoot := activationSession(t, res)

	detach := attachCLISync(sess, wsRoot, res)
	sess.EventBus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: sess.SessionID,
		TurnID:    "turn:1",
		Detail:    "hello",
		Timestamp: time.Now(),
	})
	time.Sleep(400 * time.Millisecond)
	detach()

	got := sink.String()
	if !strings.Contains(got, "stopped") {
		t.Errorf("notices = %q, want a line saying sync stopped", got)
	}
	if !strings.Contains(got, "409") {
		t.Errorf("notices = %q, want the stop line to carry the reason", got)
	}
}

// TestAttachCLISyncSaysWhenPushesStopLanding covers the gap between "running"
// and "stopped": a server that keeps failing is neither, and before this the
// user was told nothing for the whole session while the backlog queued. The
// notice must arrive once, and must clear once pushes land again.
func TestAttachCLISyncSaysWhenPushesStopLanding(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: "cli-degraded-1", Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: r.PathValue("id"), Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(chatsync.ErrorEnvelope{StatusCode: 500, Error: "Internal Server Error"})
			return
		}
		var req struct {
			Events []chatsync.EventItem `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := int64(0)
		if n := len(req.Events); n > 0 {
			last = req.Events[n-1].Seq
		}
		_ = json.NewEncoder(w).Encode(chatsync.AppendResult{InsertedCount: len(req.Events), LastSeq: last})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sink := captureSyncNotices(t)
	res := &config.Resolved{Sync: config.ResolvedSync{APIURL: srv.URL, PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 50}}
	installTestAuthToken(t)
	sess, wsRoot := activationSession(t, res)

	detach := attachCLISync(sess, wsRoot, res)
	defer detach()
	sess.EventBus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: sess.SessionID,
		TurnID:    "turn:1",
		Detail:    "hello",
		Timestamp: time.Now(),
	})

	waitForNoticeText(t, sink, "degraded")
	failing.Store(false)
	waitForNoticeText(t, sink, "recovered")

	got := sink.String()
	if n := strings.Count(got, "degraded"); n != 1 {
		t.Errorf("notices = %q, want exactly one degraded line, got %d", got, n)
	}
	if strings.Contains(got, "stopped") {
		t.Errorf("notices = %q, a degraded sync is not a stopped one", got)
	}
}

func waitForNoticeText(t *testing.T, sink *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sink.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no notice containing %q within 5s; notices = %q", want, sink.String())
}
