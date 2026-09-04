package uiadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
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
