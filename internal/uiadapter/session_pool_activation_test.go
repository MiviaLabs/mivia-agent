package uiadapter_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// activationPool drives the real pool wiring against the shared mock API and
// reports how many remote sessions were created. A create is the observable
// that proves sync started: nothing else in the wiring produces one.
func activationPool(t *testing.T, cfg config.ResolvedSync) int {
	t.Helper()
	var mu sync.Mutex
	var createdIDs []string
	sessionEvents := make(map[string][]chatsync.EventItem)
	srv := setupSyncMockServer(&mu, &createdIDs, sessionEvents)
	defer srv.Close()

	cfg.APIURL = srv.URL
	res := &config.Resolved{Model: "test-model", Sync: cfg}

	sess := chat.NewSession(res, nil)
	sess.SessionID = "pool-activation-1"
	sess.SessionDir = t.TempDir()
	sess.EventBus = events.New()

	// WorkspaceRoot, not nil agentState: attachSyncLocked resolves chat-sync's
	// identity/outbox anchor from p.agentState.WorkspaceRoot (never from
	// sess.SessionDir, which real context-enabled sessions always null - see
	// poolSyncOptions's doc comment). A nil agentState leaves wsRoot empty,
	// which now correctly refuses to sync rather than writing under a
	// relative ".mivia" path off the test binary's cwd.
	pool := uiadapter.NewSessionPool(sess, res, &cliagents.AgentSessionState{WorkspaceRoot: t.TempDir()}, false)
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.ReleaseLeases(ctx)

	mu.Lock()
	defer mu.Unlock()
	return len(createdIDs)
}

// TestSessionPoolSyncRunsWhenLoggedInWithoutExplicitEnable is the TUI twin of
// TestAttachCLISyncRunsWhenLoggedInWithoutExplicitEnable: the two surfaces are
// separate wiring sites and a rule honoured by one is routinely absent from
// the other.
func TestSessionPoolSyncRunsWhenLoggedInWithoutExplicitEnable(t *testing.T) {
	installTestAuthToken(t)
	if n := activationPool(t, config.ResolvedSync{PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 100}); n != 1 {
		t.Errorf("created = %d, want 1; an authenticated session must sync without an explicit enable", n)
	}
}

// TestSessionPoolSyncOptOutWhenExplicitlyDisabled pins the opt-out on the TUI
// surface.
func TestSessionPoolSyncOptOutWhenExplicitlyDisabled(t *testing.T) {
	installTestAuthToken(t)
	cfg := config.ResolvedSync{Disabled: true, PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 100}
	if n := activationPool(t, cfg); n != 0 {
		t.Errorf("created = %d, want 0; `enabled = false` is an explicit opt-out", n)
	}
}

// TestSessionPoolSyncSkipsWhenLoggedOut pins the fail-closed half on the TUI
// surface.
func TestSessionPoolSyncSkipsWhenLoggedOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.ResolvedSync{PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 100}
	if n := activationPool(t, cfg); n != 0 {
		t.Errorf("created = %d, want 0; a logged-out user must never upload", n)
	}
}
