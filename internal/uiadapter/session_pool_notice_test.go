package uiadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// waitForNotice reads one notice, or fails the test if none arrives. The
// timeout is the whole point: a notice nobody can observe is the defect.
func waitForNotice(t *testing.T, ch <-chan uievent.Event, what string) uievent.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatalf("no notice for %s within the timeout", what)
		return uievent.Event{}
	}
}

func noticeText(t *testing.T, ev uievent.Event) string {
	t.Helper()
	body, ok := ev.Body.(uievent.NoticeBody)
	if !ok {
		t.Fatalf("notice body = %T, want uievent.NoticeBody", ev.Body)
	}
	return body.Text
}

// TestSessionPoolNoticesSayncIsRunning pins that the pool satisfies the
// ports.Notices seam and announces a live sync on it. The seam matters as
// much as the text: internal/ui may not import internal/uiadapter, so a
// notice that does not travel through ports and uievent cannot reach the UI
// at all (INV-TUI-29).
func TestSessionPoolNoticesSyncIsRunning(t *testing.T) {
	var mu sync.Mutex
	var createdIDs []string
	sessionEvents := make(map[string][]chatsync.EventItem)
	srv := setupSyncMockServer(&mu, &createdIDs, sessionEvents)
	defer srv.Close()

	installTestAuthToken(t)
	res := &config.Resolved{Model: "test-model", Sync: config.ResolvedSync{
		APIURL: srv.URL, PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 100,
	}}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "pool-notice-1"
	sess.SessionDir = t.TempDir()
	sess.EventBus = events.New()

	pool := uiadapter.NewSessionPool(sess, res, &cliagents.AgentSessionState{WorkspaceRoot: t.TempDir()}, false)
	var notices ports.Notices = pool

	ev := waitForNotice(t, notices.Notices(), "a started sync")
	if ev.Kind != uievent.KindNotice {
		t.Errorf("Kind = %q, want %q", ev.Kind, uievent.KindNotice)
	}
	if text := noticeText(t, ev); !strings.Contains(strings.ToLower(text), "sync") {
		t.Errorf("notice = %q, want it to name sync", text)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.ReleaseLeases(ctx)
}

// TestSessionPoolNoticesSyncStop is the TUI half of "stop syncing and SAY
// SO". The plain-CLI surface writes to stderr; the TUI has no stderr to
// write to, so the same fact has to reach the user through the notice
// stream or not at all.
func TestSessionPoolNoticesSyncStop(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: "pool-stop-1", Status: "running"})
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

	installTestAuthToken(t)
	res := &config.Resolved{Model: "test-model", Sync: config.ResolvedSync{
		APIURL: srv.URL, PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 100,
	}}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "pool-stop-1"
	sess.SessionDir = t.TempDir()
	sess.EventBus = events.New()

	pool := uiadapter.NewSessionPool(sess, res, &cliagents.AgentSessionState{WorkspaceRoot: t.TempDir()}, false)
	ch := pool.Notices()
	waitForNotice(t, ch, "a started sync")

	sess.EventBus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: sess.SessionID,
		TurnID:    "turn:1",
		Detail:    "hello",
		Timestamp: time.Now(),
	})

	ev := waitForNotice(t, ch, "a stopped sync")
	text := noticeText(t, ev)
	if !strings.Contains(strings.ToLower(text), "stopped") {
		t.Errorf("notice = %q, want it to say sync stopped", text)
	}
	if !strings.Contains(text, "409") {
		t.Errorf("notice = %q, want it to carry the reason", text)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.ReleaseLeases(ctx)
}
