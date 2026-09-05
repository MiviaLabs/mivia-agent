package uiadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// installTestAuthTokenInternal points HOME at a temp dir holding a valid,
// unexpired CLI session so chatsync.DefaultTokenProvider (and
// AuthorUserIDProvider's default) resolve a real, non-nil provider without a
// network round trip. It duplicates session_pool_sync_test.go's
// installTestAuthToken (package uiadapter_test, not reachable from here)
// rather than exporting a shared test helper across the black-box/white-box
// package split this directory already uses.
func installTestAuthTokenInternal(t *testing.T) {
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

// TestSessionBindingFactory_RetriesWithConfiguredModelWhileLoading covers
// sessionBindingFactory's IsLoading fallback retry (session_pool.go's
// buildModelBindingVar(sess, res, ".", res.ProviderName, res.Model, state)
// call inside the `sess.IsLoading()` branch): a session resuming from disk
// requests its OWN saved provider/model, that pair is no longer selectable
// against the current config (the first buildModelBindingVar call fails),
// and - because the session is mid-Load rather than an explicit user /model
// switch - the factory must retry with the CURRENTLY CONFIGURED
// provider/model instead of failing the whole resume closed.
func TestSessionBindingFactory_RetriesWithConfiguredModelWhileLoading(t *testing.T) {
	prev := buildModelBindingVar
	t.Cleanup(func() { buildModelBindingVar = prev })

	res := &config.Resolved{ProviderName: "configured-provider", Model: "configured-model"}
	var calls []string
	buildModelBindingVar = func(_ *chat.Session, res *config.Resolved, _, providerName, model string, _ *cliagents.AgentSessionState) (chat.ModelBinding, error) {
		calls = append(calls, providerName+"/"+model)
		if providerName == res.ProviderName && model == res.Model {
			return chat.ModelBinding{ProviderName: providerName, Model: model}, nil
		}
		return chat.ModelBinding{}, errors.New("model is not selectable for provider " + providerName)
	}

	sess := chat.NewSession(res, nil)
	sess.SetBindingFactory(sessionBindingFactory(sess, res, nil))

	release, err := sess.BeginSessionLoad()
	if err != nil {
		t.Fatalf("BeginSessionLoad: %v", err)
	}
	defer release()

	binding, ok, err := sess.PrepareBinding("stale-provider", "stale-model")
	if err != nil {
		t.Fatalf("PrepareBinding: %v", err)
	}
	if !ok {
		t.Fatal("PrepareBinding reported no factory installed")
	}
	if binding.ProviderName != res.ProviderName || binding.Model != res.Model {
		t.Errorf("binding = %+v, want the fallback to the configured provider/model %s/%s", binding, res.ProviderName, res.Model)
	}
	want := []string{"stale-provider/stale-model", res.ProviderName + "/" + res.Model}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Errorf("buildModelBindingVar calls = %v, want %v (first the requested pair, then the retry with the configured pair)", calls, want)
	}
}

// TestSessionPool_ReleaseLeasesStopsWatcher covers ReleaseLeases's
// `if p.watcher != nil { p.watcher.Stop(ctx) }` tail: a pool that armed a
// background RemoteInputWatcher (via StartBackgroundWatch in production)
// must stop it on shutdown, or its pollers outlive the pool.
func TestSessionPool_ReleaseLeasesStopsWatcher(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	sess.SessionID = "sess-release-watcher"
	pool := NewSessionPool(sess, nil, nil, false)

	w := NewRemoteInputWatcher(WatcherConfig{Max: 1})
	pool.mu.Lock()
	pool.watcher = w
	pool.mu.Unlock()

	pool.ReleaseLeases(context.Background())

	w.mu.Lock()
	stopped := w.stopped
	w.mu.Unlock()
	if !stopped {
		t.Fatal("ReleaseLeases did not stop the pool's armed watcher")
	}
}

// TestSessionPool_AttachSyncLockedStopsWatcherEntryForSameID covers
// attachSyncLocked's `if p.watcher != nil { p.watcher.StopSync(id, ...) }`
// branch: (re)attaching chat sync for a session must hand that session's
// unpooled-watch poller (if StartBackgroundWatch happened to be watching it)
// back to pooled sync, not run both at once.
func TestSessionPool_AttachSyncLockedStopsWatcherEntryForSameID(t *testing.T) {
	res := &config.Resolved{}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "sess-watcher-stop"
	sess.EventBus = events.New()
	pool := NewSessionPool(sess, res, nil, false)

	w := NewRemoteInputWatcher(WatcherConfig{Max: 1})
	// Presence alone is what StopSync's `poller, ok := w.watching[sessionID]`
	// branch reads; StopSync's own found/not-found behavior is already
	// covered by remote_input_watcher_test.go.
	w.watching[sess.SessionID] = nil

	pool.mu.Lock()
	pool.watcher = w
	pool.attachSyncLocked(sess)
	pool.mu.Unlock()

	w.mu.Lock()
	_, stillWatching := w.watching[sess.SessionID]
	w.mu.Unlock()
	if stillWatching {
		t.Fatal("attachSyncLocked did not stop the watcher's own entry for the session it just (re)attached")
	}
}

// TestSessionPool_StartBackgroundWatchNoopWhenSyncInactive covers the
// `if !p.res.Sync.Active(...) { return }` early exit: with sync explicitly
// disabled, StartBackgroundWatch must not arm a watcher at all.
func TestSessionPool_StartBackgroundWatchNoopWhenSyncInactive(t *testing.T) {
	res := &config.Resolved{Sync: config.ResolvedSync{Disabled: true}}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "seed-inactive"
	pool := NewSessionPool(sess, res, nil, false)

	pool.StartBackgroundWatch(context.Background())

	pool.mu.Lock()
	w := pool.watcher
	pool.mu.Unlock()
	if w != nil {
		t.Fatal("StartBackgroundWatch armed a watcher while sync is disabled")
	}
}

// TestSessionPool_StartBackgroundWatchNoopWhenNoSeedSession covers the
// `if seed == nil { return }` early exit: a pool with sync active but no
// pooled session yet (nothing StartBackgroundWatch could use as ListSessions
// scope) must not arm a watcher.
func TestSessionPool_StartBackgroundWatchNoopWhenNoSeedSession(t *testing.T) {
	installTestAuthTokenInternal(t)
	res := &config.Resolved{}
	pool := NewSessionPool(nil, res, nil, false)

	pool.StartBackgroundWatch(context.Background())

	pool.mu.Lock()
	w := pool.watcher
	pool.mu.Unlock()
	if w != nil {
		t.Fatal("StartBackgroundWatch armed a watcher with no pooled session to seed it")
	}
}

// newBackfillInputServer answers /v1/chat-sessions/{id}/inputs/next with one
// real RemoteInput for remoteID (once) and its matching consume endpoint,
// mirroring newMockInputServer in remote_input_watcher_test.go but
// parameterized on the remote session id StartBackgroundWatch's own
// WatcherConfig.Backfill discovers, instead of a fixed one.
func newBackfillInputServer(t *testing.T, remoteID string) *httptest.Server {
	t.Helper()
	var consumed int32
	in := &chatsync.SessionInput{
		ID: "inp-bg-1", SessionID: remoteID, Kind: "message",
		Body: "hello from background watch", AuthorUserID: "auth-1",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != remoteID || atomic.LoadInt32(&consumed) != 0 {
			_ = json.NewEncoder(w).Encode(chatsync.NextInput{})
			return
		}
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: in})
	})
	// The consume response must carry the SAME AuthorUserID/Body/SessionID as
	// the /next response: InputPoller.deliver's validateRemoteInput trusts
	// only what THIS response says, not what /next said, so a thin consume
	// reply (id-only) fails validation and the input is silently dropped
	// (see newMockInputServer in remote_input_watcher_test.go, which returns
	// the same full object from both endpoints for the same reason).
	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&consumed, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(in)
	})
	return httptest.NewServer(mux)
}

// TestSessionPool_StartBackgroundWatchDeliversRealInput drives
// StartBackgroundWatch end to end: a real *chat.Session backed by a real
// SQLite context store (newRealCatalogSession, shared with
// remote_input_watcher_test.go's Backfill fixtures) seeds the pool, a second
// unpooled sibling session ("cand-bg-1") carries a saved sync identity with a
// RemoteSessionID, and the watcher StartBackgroundWatch builds must discover
// it, poll it, and deliver its input onto pool.RemoteInputs() - exercising
// both the IsPooled closure (returns false for the unpooled candidate, true
// would exclude it) and the Deliver closure (forwards the real
// chatsync.RemoteInput onto the pool's ports.RemoteInputEvent channel) that
// WatcherConfig wires from session_pool.go's StartBackgroundWatch.
func TestSessionPool_StartBackgroundWatchDeliversRealInput(t *testing.T) {
	installTestAuthTokenInternal(t)
	orig := AuthorUserIDProvider
	AuthorUserIDProvider = func() chatsync.AuthorUserIDProvider {
		return func(context.Context) (string, error) { return "auth-1", nil }
	}
	t.Cleanup(func() { AuthorUserIDProvider = orig })

	wsRoot := t.TempDir()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	seed := newRealCatalogSession(t, store, "sess-seed-bg")
	newRealCatalogSession(t, store, "cand-bg-1")
	setupTestIdentity(t, wsRoot, "cand-bg-1", "remote-cand-bg-1")

	server := newBackfillInputServer(t, "remote-cand-bg-1")
	defer server.Close()

	res := backfillTestRes(server.URL)
	res.Sync.BackgroundWatchMax = 8

	pool := NewSessionPool(seed, res, &cliagents.AgentSessionState{WorkspaceRoot: wsRoot}, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.StartBackgroundWatch(ctx)

	select {
	case ev := <-pool.RemoteInputs():
		if ev.SessionID != "cand-bg-1" || ev.Body != "hello from background watch" {
			t.Errorf("delivered event = %+v, want SessionID=cand-bg-1 Body=%q", ev, "hello from background watch")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for StartBackgroundWatch to deliver a real remote input")
	}

	pool.mu.Lock()
	w := pool.watcher
	pool.mu.Unlock()
	if w == nil {
		t.Fatal("StartBackgroundWatch did not arm a watcher")
	}
	w.Stop(ctx)
}
