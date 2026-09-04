package uiadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// withFixedAuthorUserIDProvider overrides uiadapter.AuthorUserIDProvider for
// the duration of a test, so no test depends on a real logged-in miviaauth
// session or a network call to Whoami.
func withFixedAuthorUserIDProvider(t *testing.T, id string) {
	t.Helper()
	orig := uiadapter.AuthorUserIDProvider
	uiadapter.AuthorUserIDProvider = func() chatsync.AuthorUserIDProvider {
		return func(context.Context) (string, error) { return id, nil }
	}
	t.Cleanup(func() { uiadapter.AuthorUserIDProvider = orig })
}

func remoteInputMockServer(t *testing.T, input chatsync.SessionInput) (*httptest.Server, *int32) {
	t.Helper()
	var created int32
	mux := http.NewServeMux()
	served := false
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&created, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: input.SessionID, Status: "running"})
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
		w.Header().Set("Content-Type", "application/json")
		if served {
			_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: nil})
			return
		}
		served = true
		in := input
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: &in})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		now := time.Now().Format(time.RFC3339)
		out := input
		out.ConsumedAt = &now
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &created
}

func newRemoteInputPool(t *testing.T, srv *httptest.Server, sessionID string, created *int32) *uiadapter.SessionPool {
	t.Helper()
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
	sess.SessionID = sessionID
	sess.EventBus = events.New()

	pool := uiadapter.NewSessionPool(sess, res, &cliagents.AgentSessionState{WorkspaceRoot: t.TempDir()}, false)

	// The deferred attach - and with it the input poller - runs on the
	// session's first message. Without it the poller never starts, and the
	// "never forwarded" assertion passes vacuously.
	sess.EventBus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: sess.SessionID,
		TurnID:    "turn:1",
		Detail:    "the first message",
		Timestamp: time.Now(),
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(created) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(created) == 0 {
		t.Fatal("the session never attached on its first message; the poller tests prove nothing")
	}
	// The poller starts in the same goroutine, right after the create. Give
	// it a beat so the tests below observe a RUNNING poller.
	time.Sleep(50 * time.Millisecond)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pool.ReleaseLeases(ctx)
	})
	return pool
}

// TestSessionPool_RemoteInputsForwardsAuthorizedInput proves the positive
// path end to end: a remote input authored by the CLI's own verified
// principal, for the session the pool actually attached, reaches
// pool.RemoteInputs() tagged with the LOCAL chat session id.
func TestSessionPool_RemoteInputsForwardsAuthorizedInput(t *testing.T) {
	withFixedAuthorUserIDProvider(t, "user-1")
	srv, created := remoteInputMockServer(t, chatsync.SessionInput{
		ID: "input-1", SessionID: "remote-authorized-1", AuthorUserID: "user-1",
		Kind: "message", Body: "hello from the tablet",
	})
	pool := newRemoteInputPool(t, srv, "local-authorized-1", created)

	select {
	case ev := <-pool.RemoteInputs():
		if ev.SessionID != "local-authorized-1" {
			t.Errorf("RemoteInputEvent.SessionID = %q, want the LOCAL chat session id %q", ev.SessionID, "local-authorized-1")
		}
		if ev.Body != "hello from the tablet" {
			t.Errorf("RemoteInputEvent.Body = %q", ev.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the authorized remote input to be forwarded")
	}
}

// TestSessionPool_RemoteInputsRejectsUnverifiedAuthor is the DC-13 successor
// of the deleted TestSessionPool_DoesNotExecuteRemoteInput. Polling is now
// intentionally enabled (poolSyncOptions), so the safety property to pin is
// no longer "the pool never polls" - it is "an input whose author does not
// match the CLI's own verified principal is never forwarded onto
// RemoteInputs(), no matter what body it carries". SessionPool has no
// reference to Conversation.Send at all (see remote_input.go), so proving
// nothing reaches RemoteInputs() is equivalent to proving nothing can become
// a local turn from this package.
func TestSessionPool_RemoteInputsRejectsUnverifiedAuthor(t *testing.T) {
	withFixedAuthorUserIDProvider(t, "user-1")
	srv, created := remoteInputMockServer(t, chatsync.SessionInput{
		ID: "input-attacker", SessionID: "remote-attacker-1", AuthorUserID: "someone-else",
		Kind: "message", Body: "rm -rf /",
	})
	pool := newRemoteInputPool(t, srv, "local-attacker-1", created)

	select {
	case ev := <-pool.RemoteInputs():
		t.Fatalf("unexpected forwarded input from an unverified author: %+v", ev)
	case <-time.After(500 * time.Millisecond):
		// Expected: the mismatched author never reaches the port.
	}
}
