package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// newAuthGuardedServer answers every request that arrives without a bearer
// Authorization header with 401, the way /v1/chat-sessions does, and records
// that it happened. Recording is the point: a client that fails open sends an
// unauthenticated request and then reports a plain "unauthorized" error, which
// is indistinguishable from an expired token unless the fake remembers.
// Session reads return a session object; the append-ack readback (a GET that
// returns an ARRAY) has its own shape, or a short-ack verification after a
// real append decodes garbage and reports an unrelated upload failure.
func newAuthGuardedServer(t *testing.T, unauthenticated *atomic.Int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		// A full ack for whatever batch arrives, so a real append verifies
		// cleanly; without it the short-ack readback turns the auth assertion
		// into an unrelated upload failure.
		var req struct {
			Events []struct {
				Seq int64 `json:"seq"`
			} `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := int64(0)
		for _, ev := range req.Events {
			if ev.Seq > last {
				last = ev.Seq
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{LastSeq: last, InsertedCount: len(req.Events)})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sess-auth-1","status":"running","lastSeq":0}`))
	})
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			unauthenticated.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(guarded)
	t.Cleanup(srv.Close)
	return srv
}

// TestOpenSessionRefusesWithoutTokenProvider pins the fail-closed half of the
// contract: with no way to authenticate, OpenSession must refuse to construct
// rather than start a sync that uploads conversation content anonymously.
func TestOpenSessionRefusesWithoutTokenProvider(t *testing.T) {
	var unauthenticated atomic.Int64
	srv := newAuthGuardedServer(t, &unauthenticated)

	opts := SessionOptions{
		ClientOptions: ClientOptions{BaseURL: srv.URL},
		OutboxDir:     filepath.Join(t.TempDir(), "outbox-noauth"),
		CreateTitle:   "No Auth",
	}

	sess, err := OpenSession(context.Background(), events.New(), "local-1", opts)
	if err == nil {
		_ = sess.Stop(context.Background())
		t.Fatal("OpenSession() error = nil; a session with no token provider must refuse to start")
	}
	if n := unauthenticated.Load(); n != 0 {
		t.Errorf("server saw %d unauthenticated request(s); refusal must happen before any upload", n)
	}
}

// TestOpenSessionAuthenticatesEveryRequest drives the constructor both
// production sites call and asserts the server never sees a request without a
// bearer token. The requests exist only once the first message attaches, so
// the test sends one - otherwise the no-request state would make the
// assertion pass vacuously.
func TestOpenSessionAuthenticatesEveryRequest(t *testing.T) {
	var unauthenticated atomic.Int64
	srv := newAuthGuardedServer(t, &unauthenticated)

	bus := events.New()
	opts := SessionOptions{
		TokenProvider: func(context.Context, bool) (string, error) { return "tok-1", nil },
		ClientOptions: ClientOptions{BaseURL: srv.URL},
		OutboxDir:     filepath.Join(t.TempDir(), "outbox-auth"),
		CreateTitle:   "With Auth",
	}

	sess, err := OpenSession(context.Background(), bus, "local-1", opts)
	if err != nil {
		t.Fatalf("OpenSession() error = %v, want nil", err)
	}
	publishTurnStart(bus, "local-1", "turn:1", "the message that attaches")
	waitUntil(t, "the deferred attach to reach the server", func() bool {
		return sess.LastSeq() >= 1
	})
	if err := sess.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if n := unauthenticated.Load(); n != 0 {
		t.Errorf("server saw %d unauthenticated request(s), want 0", n)
	}
}

// TestNewClientRefusesNilTokenProvider pins the constructor guard directly:
// the provider is positional so it cannot be omitted by accident, and nil is
// refused so it cannot be zeroed by accident either.
func TestNewClientRefusesNilTokenProvider(t *testing.T) {
	if _, err := NewClient(nil, ClientOptions{BaseURL: "https://api.example.com"}); err == nil {
		t.Fatal("NewClient(nil, ...) error = nil, want refusal")
	}
}

// newTestClient builds a Client with a stub token provider. Tests that are
// not about auth still need one, because the constructor refuses nil.
func newTestClient(t *testing.T, opts ClientOptions) *Client {
	t.Helper()
	c, err := NewClient(func(context.Context, bool) (string, error) { return "test-token", nil }, opts)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return c
}

// testTokenProvider is the stub SessionOptions literals use.
func testTokenProvider(context.Context, bool) (string, error) { return "test-token", nil }
