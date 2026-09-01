package clichat

import (
	"context"
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

// syncActivationServer answers the create/heartbeat pair the sync wiring makes
// on attach, and counts the creates. A create is the observable that proves
// sync actually started: it is the first authenticated request OpenSession
// makes, and nothing else in the wiring can produce one.
func syncActivationServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var created int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&created, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: "cli-activation-1", Status: "running"})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &created
}

// activationSession returns a session ready for attachCLISync, plus the
// wsRoot to pass alongside it. wsRoot is returned separately, not stashed on
// sess.SessionDir: production never has a real SessionDir once context state
// is enabled (see cliSyncOptions's doc comment), and this helper mirrors that.
func activationSession(t *testing.T, res *config.Resolved) (*chat.Session, string) {
	t.Helper()
	sess := chat.NewSession(res, nil)
	sess.SessionID = "cli-activation-1"
	sess.EventBus = events.New()
	return sess, t.TempDir()
}

// TestAttachCLISyncRunsWhenLoggedInWithoutExplicitEnable pins the product
// decision: a logged-in user gets sync with no flag, no prompt, and no
// `enabled = true` in their config. The [sync] table here sets only the
// endpoint and the bounds; the enable key is absent, which is the state
// every real user's config is in.
func TestAttachCLISyncRunsWhenLoggedInWithoutExplicitEnable(t *testing.T) {
	srv, created := syncActivationServer(t)
	res := &config.Resolved{
		Sync: config.ResolvedSync{
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     50,
		},
	}
	installTestAuthToken(t)
	sess, wsRoot := activationSession(t, res)

	detach := attachCLISync(sess, wsRoot, res)
	time.Sleep(50 * time.Millisecond)
	detach()

	if n := atomic.LoadInt32(created); n != 1 {
		t.Errorf("created = %d, want 1; an authenticated session must sync without an explicit enable", n)
	}
}

// TestAttachCLISyncOptOutWhenExplicitlyDisabled pins the other half: a user
// who writes `enabled = false` gets a local-only session even while logged
// in. An explicit false must stay distinguishable from an absent key.
func TestAttachCLISyncOptOutWhenExplicitlyDisabled(t *testing.T) {
	srv, created := syncActivationServer(t)
	res := &config.Resolved{
		Sync: config.ResolvedSync{
			Disabled:         true,
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     50,
		},
	}
	installTestAuthToken(t)
	sess, wsRoot := activationSession(t, res)

	detach := attachCLISync(sess, wsRoot, res)
	time.Sleep(50 * time.Millisecond)
	detach()

	if n := atomic.LoadInt32(created); n != 0 {
		t.Errorf("created = %d, want 0; `enabled = false` is an explicit opt-out", n)
	}
}

// TestAttachCLISyncSkipsWhenLoggedOut keeps the fail-closed half of the
// decision: no local session means no upload, silently and with no error.
func TestAttachCLISyncSkipsWhenLoggedOut(t *testing.T) {
	srv, created := syncActivationServer(t)
	t.Setenv("HOME", t.TempDir())
	res := &config.Resolved{
		Sync: config.ResolvedSync{
			APIURL:           srv.URL,
			PollWaitSeconds:  1,
			HeartbeatSeconds: 1,
			MaxUnflushed:     50,
		},
	}
	sess, wsRoot := activationSession(t, res)

	detach := attachCLISync(sess, wsRoot, res)
	time.Sleep(50 * time.Millisecond)
	detach()

	if n := atomic.LoadInt32(created); n != 0 {
		t.Errorf("created = %d, want 0; a logged-out user must never upload", n)
	}
}

// TestCLISyncOptionsDefaultsBaseURLToAPIRoot pins that an unset api_url still
// reaches the real API. Nothing asks the user to configure sync any more, so
// an empty api_url is the normal case, and it must resolve through the one
// source that already knows where the API lives (miviaauth.ServerURLFromEnv)
// rather than a second hardcoded copy.
func TestCLISyncOptionsDefaultsBaseURLToAPIRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	res := &config.Resolved{Sync: config.ResolvedSync{PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 50}}
	sess, wsRoot := activationSession(t, res)

	opts := cliSyncOptions(sess, wsRoot, res, func(_ context.Context, _ bool) (string, error) { return "t", nil })

	want := miviaauth.ServerURLFromEnv()
	if strings.TrimSpace(want) == "" {
		t.Fatal("ServerURLFromEnv() is empty; the default would send sync nowhere")
	}
	if opts.ClientOptions.BaseURL != want {
		t.Errorf("BaseURL = %q, want %q", opts.ClientOptions.BaseURL, want)
	}
}
