package uiadapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func setupTestIdentity(t *testing.T, wsRoot, sessionID, remoteID string) {
	t.Helper()
	anchor := chatSyncAnchor(wsRoot)
	dir := chatsync.IdentityDir(anchor)
	key := chatsync.IdentityKey(sessionID)
	ident := chatsync.SyncIdentity{
		LocalHandle:     chatsync.LocalHandle("handle-" + sessionID),
		RemoteSessionID: remoteID,
		WriterID:        "writer-" + sessionID,
	}
	if err := chatsync.SaveIdentity(dir, key, ident); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
}

func TestRemoteInputWatcher_StopSync(t *testing.T) {
	wsRoot := t.TempDir()
	setupTestIdentity(t, wsRoot, "sess-1", "remote-1")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{})
	}))
	defer server.Close()

	tokens := func(ctx context.Context, refresh bool) (string, error) {
		return "test-token", nil
	}

	res := &config.Resolved{
		Sync: config.ResolvedSync{
			APIURL:             server.URL,
			PollWaitSeconds:    1,
			BackgroundWatchMax: 8,
		},
	}

	sess := chat.NewSession(res, nil)
	sess.SessionID = "sess-seed"

	cfg := WatcherConfig{
		Seed:           sess,
		Tokens:         tokens,
		Res:            res,
		WorkspaceRoot:  wsRoot,
		AuthorProvider: func(ctx context.Context) (string, error) { return "auth-1", nil },
		Max:            8,
	}

	w := NewRemoteInputWatcher(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Backfill(ctx)

	if err := w.StopSync("unwatched", 100*time.Millisecond); err != nil {
		t.Errorf("StopSync(unwatched) error = %v", err)
	}

	w.Stop(ctx)
}

// TestRemoteInputWatcher_StopSyncStopsWatchedPoller covers StopSync's other
// branch: a session actually being watched. The unwatched-session case above
// only ever hits the early "not found" return; this one exercises the
// delete-from-map-then-poller.Stop(ctx) path.
func TestRemoteInputWatcher_StopSyncStopsWatchedPoller(t *testing.T) {
	server := newMockInputServer(t)
	defer server.Close()

	tokens := func(ctx context.Context, refresh bool) (string, error) { return "test-token", nil }
	client, err := chatsync.NewClient(tokens, chatsync.ClientOptions{BaseURL: chatsync.DefaultBaseURL(server.URL)})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	poller := chatsync.NewInputPoller(client, "remote-saved-1", 1, func(ctx context.Context) (string, error) { return "auth-1", nil }, t.TempDir())

	w := NewRemoteInputWatcher(WatcherConfig{Max: 8})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	w.watching["watched-1"] = poller

	if err := w.StopSync("watched-1", time.Second); err != nil {
		t.Fatalf("StopSync(watched-1) error = %v", err)
	}
	w.mu.Lock()
	_, stillWatching := w.watching["watched-1"]
	w.mu.Unlock()
	if stillWatching {
		t.Fatal("StopSync did not remove the session from the watching set")
	}
}

func TestCommandRunner_MountSession(t *testing.T) {
	res := &config.Resolved{}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "primary"

	runner := NewCommandRunner(sess, res, nil)

	conv, err := runner.Mount("primary")
	if err != nil {
		t.Fatalf("Mount primary failed: %v", err)
	}
	if conv == nil || conv.ID() != "primary" {
		t.Errorf("expected conv ID 'primary', got %+v", conv)
	}

	var _ ports.SessionMounter = runner
}

func newMockInputServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		in := &chatsync.SessionInput{
			ID: "inp-123", SessionID: "remote-saved-1", Kind: "message",
			Body: "hello to unpooled session", AuthorUserID: "auth-1",
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/chat-sessions/remote-saved-1/inputs/next" {
			_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: in})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/chat-sessions/remote-saved-1/inputs/inp-123/consume" {
			_ = json.NewEncoder(w).Encode(in)
			return
		}
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{})
	}))
}

func TestRemoteInputWatcher_DeliversToRemoteInputs(t *testing.T) {
	wsRoot := t.TempDir()
	setupTestIdentity(t, wsRoot, "saved-local-1", "remote-saved-1")
	server := newMockInputServer(t)
	defer server.Close()

	tokens := func(ctx context.Context, refresh bool) (string, error) { return "test-token", nil }
	res := &config.Resolved{
		Sync: config.ResolvedSync{APIURL: server.URL, PollWaitSeconds: 1, BackgroundWatchMax: 8},
	}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "primary"
	pool := NewSessionPool(sess, res, &cliagents.AgentSessionState{WorkspaceRoot: wsRoot}, false)

	delivered := make(chan ports.RemoteInputEvent, 1)
	cfg := WatcherConfig{
		Seed: sess, Tokens: tokens, Res: res, WorkspaceRoot: wsRoot,
		AuthorProvider: func(ctx context.Context) (string, error) { return "auth-1", nil },
		Max:            8, IsPooled: func(id string) bool { return pool.IsActive(id) || id == "primary" },
		Deliver: func(id string, in chatsync.RemoteInput) {
			delivered <- ports.RemoteInputEvent{ID: in.ID, SessionID: id, Body: in.Body}
		},
	}

	w := NewRemoteInputWatcher(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := chatsync.NewClient(tokens, chatsync.ClientOptions{BaseURL: chatsync.DefaultBaseURL(server.URL)})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	poller := chatsync.NewInputPoller(client, "remote-saved-1", 1, cfg.AuthorProvider, t.TempDir())
	w.watching["saved-local-1"] = poller
	poller.Start(ctx)
	go func() {
		for ri := range poller.Inputs() {
			cfg.Deliver("saved-local-1", ri)
		}
	}()

	select {
	case ev := <-delivered:
		if ev.SessionID != "saved-local-1" || ev.Body != "hello to unpooled session" {
			t.Errorf("unexpected delivery: %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for watcher to deliver input")
	}
	w.Stop(ctx)
}

// backfillTestRes builds the config.Resolved every fixture session in this
// file shares: a real provider/model binding (EnsureSession's Binding.
// Validate rejects a zero BindingRevision) plus the sync polling settings
// RemoteInputWatcher.Backfill reads.
func backfillTestRes(apiURL string) *config.Resolved {
	return &config.Resolved{
		ProviderName: "fake", Model: "model", Models: []string{"model"},
		Sync: config.ResolvedSync{APIURL: apiURL, PollWaitSeconds: 1, BackgroundWatchMax: 8},
	}
}

// newRealCatalogSession wires a real *chat.Session to store under its own
// principal (sharing workspace/subject with every other fixture session so
// they land in the same ListSessions scope) and commits one real user turn
// through noticeCompleter. A bare store.EnsureSession is not enough here:
// ListSessions's query only surfaces a context_sessions row once it carries
// a source_sequence>0 or a title (see
// TestIntegrationListSessionsShowsSessionBeforeTurnCommits in
// internal/chat) - a zero-turn EnsureSession row stays invisible by design,
// so a real SendUser is what actually makes a candidate discoverable.
func newRealCatalogSession(t *testing.T, store *storage.SQLite, sessionID string) *chat.Session {
	t.Helper()
	principal, err := contextstate.NewPrincipal("ws", sessionID, "subject")
	if err != nil {
		t.Fatalf("NewPrincipal(%s): %v", sessionID, err)
	}
	sess := chat.NewSession(backfillTestRes(""), noticeCompleter{})
	sess.SessionID = sessionID
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatalf("SetContextManager(%s): %v", sessionID, err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatalf("SetContextStore(%s): %v", sessionID, err)
	}
	if _, err := sess.SendUser(context.Background(), "hello from "+sessionID, io.Discard); err != nil {
		t.Fatalf("SendUser(%s): %v", sessionID, err)
	}
	return sess
}

// newBackfillFixture builds a seed *chat.Session wired to a real SQLite
// context store, plus one real unpooled candidate session ("cand-1") in the
// same workspace/subject scope with a saved sync identity carrying a
// RemoteSessionID ("remote-cand-1") - the two facts candidates() joins to
// find a backfill target. Unlike the other tests in this file, this drives
// RemoteInputWatcher.Backfill's real discovery path (ListSessions +
// LoadIdentityReadOnly) end to end instead of pre-seeding w.watching by hand.
func newBackfillFixture(t *testing.T) (sess *chat.Session, wsRoot string) {
	t.Helper()
	wsRoot = t.TempDir()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sess = newRealCatalogSession(t, store, "sess-seed")
	newRealCatalogSession(t, store, "cand-1")
	setupTestIdentity(t, wsRoot, "cand-1", "remote-cand-1")
	return sess, wsRoot
}

func TestRemoteInputWatcher_NilReceiverIsSafe(t *testing.T) {
	var w *RemoteInputWatcher
	if err := w.StopSync("x", time.Second); err != nil {
		t.Fatalf("StopSync on nil watcher: %v", err)
	}
	w.Stop(context.Background())
	if got := w.candidates(); got != nil {
		t.Fatalf("candidates() on nil watcher = %v, want nil", got)
	}
	w.Backfill(context.Background())
}

func TestRemoteInputWatcher_CandidatesNilSeed(t *testing.T) {
	w := NewRemoteInputWatcher(WatcherConfig{})
	if got := w.candidates(); got != nil {
		t.Fatalf("candidates() with nil Seed = %v, want nil", got)
	}
}

func TestRemoteInputWatcher_CandidatesEmptyWorkspaceRoot(t *testing.T) {
	sess, _ := newBackfillFixture(t)
	w := NewRemoteInputWatcher(WatcherConfig{Seed: sess, WorkspaceRoot: ""})
	if got := w.candidates(); got != nil {
		t.Fatalf("candidates() with empty WorkspaceRoot = %v, want nil (chatSyncAnchor/IdentityDir both empty)", got)
	}
}

func TestRemoteInputWatcher_BackfillNoopsWithoutTokens(t *testing.T) {
	sess, wsRoot := newBackfillFixture(t)
	w := NewRemoteInputWatcher(WatcherConfig{Seed: sess, WorkspaceRoot: wsRoot, Max: 8})
	w.Backfill(context.Background())
	if len(w.watching) != 0 {
		t.Fatalf("watching = %d entries, want 0 when cfg.Tokens is nil", len(w.watching))
	}
}

func TestRemoteInputWatcher_BackfillStoppedOrFull(t *testing.T) {
	// stopped: Backfill must return before ever touching cfg.Seed (nil
	// here would panic candidates() if this guard were missing).
	stoppedW := NewRemoteInputWatcher(WatcherConfig{Max: 1})
	stoppedW.Stop(context.Background())
	stoppedW.Backfill(context.Background())
	if len(stoppedW.watching) != 0 {
		t.Fatalf("stopped watcher started watching: %d entries", len(stoppedW.watching))
	}

	// full: slots <= 0 must return before touching cfg.Seed (nil here too).
	fullW := NewRemoteInputWatcher(WatcherConfig{Max: 1})
	fullW.watching["already-watching"] = nil
	fullW.Backfill(context.Background())
	if len(fullW.watching) != 1 {
		t.Fatalf("full watcher's watching set changed: %d entries, want 1", len(fullW.watching))
	}
}

// TestRemoteInputWatcher_BackfillDiscoversAndWatchesRealCandidates drives
// Backfill's real discovery path end to end: ListSessions on the seed finds
// two real chat_sessions rows sharing its workspace/subject, candidates()
// resolves each to its saved sync identity, and Backfill starts a real
// poller for the one that is not already pooled.
//
// The already-pooled candidate is deliberately excluded by IsPooled only on
// the SECOND call for its ID (the Backfill-loop check at the poller-creation
// site), not the first (the candidates()-time check): this exercises both
// IsPooled guards as the independent, non-redundant checks they are - a
// session can go from unpooled to pooled between candidates() enumerating it
// and Backfill reaching it in the loop, and either guard alone would miss
// that window.
// newSingleInputServer serves remoteID a real input once (matched on its
// GET .../inputs/next and POST .../inputs/<inputID>/consume paths), then
// empties out. Shared by tests that need Backfill's own poller (not a
// manually-injected one) to actually deliver something.
func newSingleInputServer(t *testing.T, remoteID, inputID, body string) *httptest.Server {
	t.Helper()
	nextPath := "/v1/chat-sessions/" + remoteID + "/inputs/next"
	consumePath := "/v1/chat-sessions/" + remoteID + "/inputs/" + inputID + "/consume"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		in := &chatsync.SessionInput{ID: inputID, SessionID: remoteID, Kind: "message", Body: body, AuthorUserID: "auth-1"}
		if r.Method == http.MethodGet && r.URL.Path == nextPath {
			_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: in})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == consumePath {
			_ = json.NewEncoder(w).Encode(in)
			return
		}
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{})
	}))
}

func TestRemoteInputWatcher_BackfillDiscoversAndWatchesRealCandidates(t *testing.T) {
	sess, wsRoot := newBackfillFixture(t)

	store := sess.ContextStore().(*storage.SQLite)
	newRealCatalogSession(t, store, "cand-pooled")
	setupTestIdentity(t, wsRoot, "cand-pooled", "remote-pooled")

	// "remote-cand-1" is cand-1's saved RemoteSessionID: this drives the
	// Backfill-started poller's Inputs() channel for real, exercising the
	// delivery goroutine's body (not just its spawn) the same way
	// TestRemoteInputWatcher_DeliversToRemoteInputs does for a manually-
	// injected poller.
	const deliveredBody = "hello from Backfill's own poller"
	server := newSingleInputServer(t, "remote-cand-1", "inp-cand-1", deliveredBody)
	defer server.Close()

	pooledCalls := 0
	isPooled := func(id string) bool {
		if id != "cand-pooled" {
			return false
		}
		pooledCalls++
		return pooledCalls > 1
	}

	delivered := make(chan ports.RemoteInputEvent, 1)
	res := &config.Resolved{Sync: config.ResolvedSync{APIURL: server.URL, PollWaitSeconds: 1, BackgroundWatchMax: 8}}
	tokens := func(ctx context.Context, refresh bool) (string, error) { return "test-token", nil }
	cfg := WatcherConfig{
		Seed: sess, Tokens: tokens, Res: res, WorkspaceRoot: wsRoot,
		AuthorProvider: func(ctx context.Context) (string, error) { return "auth-1", nil },
		Max:            8, IsPooled: isPooled,
		Deliver: func(id string, in chatsync.RemoteInput) {
			delivered <- ports.RemoteInputEvent{ID: in.ID, SessionID: id, Body: in.Body}
		},
	}
	w := NewRemoteInputWatcher(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Backfill(ctx)

	select {
	case ev := <-delivered:
		if ev.SessionID != "cand-1" || ev.Body != deliveredBody {
			t.Errorf("unexpected delivery: %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Backfill's own poller to deliver an input")
	}

	w.mu.Lock()
	_, watchingCand := w.watching["cand-1"]
	_, watchingPooled := w.watching["cand-pooled"]
	watchCount := len(w.watching)
	w.mu.Unlock()
	if !watchingCand {
		t.Fatal("Backfill did not start a poller for the real unpooled candidate")
	}
	if watchingPooled {
		t.Fatal("Backfill started a poller for a candidate the loop-time IsPooled check rejected")
	}
	if watchCount != 1 {
		t.Fatalf("watching count = %d, want 1", watchCount)
	}

	// A second Backfill call must not duplicate the already-watched poller
	// (the "already watching this session" continue in the loop).
	w.Backfill(ctx)
	w.mu.Lock()
	watchCount = len(w.watching)
	w.mu.Unlock()
	if watchCount != 1 {
		t.Fatalf("second Backfill changed watching count to %d, want still 1 (no duplicate)", watchCount)
	}

	w.Stop(ctx)
}

// TestRemoteInputWatcher_BackfillAbortsIfStoppedWhileEnumerating covers the
// second `if w.stopped` check inside Backfill: w.candidates() runs with the
// lock released (a session catalog list can be slow), so Stop can land in
// that exact window. IsPooled is the one hook Backfill calls from inside
// candidates() while unlocked, so calling w.Stop from it deterministically
// reproduces that window without a sleep-based race.
func TestRemoteInputWatcher_BackfillAbortsIfStoppedWhileEnumerating(t *testing.T) {
	sess, wsRoot := newBackfillFixture(t)
	res := backfillTestRes("")
	tokens := func(ctx context.Context, refresh bool) (string, error) { return "test-token", nil }

	var w *RemoteInputWatcher
	cfg := WatcherConfig{
		Seed: sess, Tokens: tokens, Res: res, WorkspaceRoot: wsRoot, Max: 8,
		IsPooled: func(id string) bool {
			w.Stop(context.Background())
			return false
		},
	}
	w = NewRemoteInputWatcher(cfg)

	w.Backfill(context.Background())

	if len(w.watching) != 0 {
		t.Fatalf("Backfill started %d poller(s) after being stopped mid-enumeration, want 0", len(w.watching))
	}
}

// TestRemoteInputWatcher_BackfillStopsAtMaxMidLoop covers the loop's own
// `len(w.watching) >= w.cfg.Max` break, distinct from the pre-loop slots<=0
// short-circuit other tests already cover: two real unpooled candidates, a
// cap of 1, so the second iteration's check (not the entry check) is what
// stops it.
func TestRemoteInputWatcher_BackfillStopsAtMaxMidLoop(t *testing.T) {
	sess, wsRoot := newBackfillFixture(t)
	store := sess.ContextStore().(*storage.SQLite)
	newRealCatalogSession(t, store, "cand-2")
	setupTestIdentity(t, wsRoot, "cand-2", "remote-cand-2")

	server := newMockInputServer(t)
	defer server.Close()

	res := backfillTestRes(server.URL)
	tokens := func(ctx context.Context, refresh bool) (string, error) { return "test-token", nil }
	w := NewRemoteInputWatcher(WatcherConfig{
		Seed: sess, Tokens: tokens, Res: res, WorkspaceRoot: wsRoot,
		AuthorProvider: func(ctx context.Context) (string, error) { return "auth-1", nil },
		Max:            1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Backfill(ctx)

	w.mu.Lock()
	watchCount := len(w.watching)
	w.mu.Unlock()
	if watchCount != 1 {
		t.Fatalf("watching count = %d, want exactly 1 (Max)", watchCount)
	}
	w.Stop(ctx)
}
