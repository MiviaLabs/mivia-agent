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

// stubBusRegistrar is a test double for uiadapter.SessionBusRegistrar that
// records every bind/unbind call. internal/uiadapter must never import
// internal/cli (INV-TUI-29), so this test exercises the indirection var
// directly, exactly as internal/newtui/run.go's production wiring does.
type stubBusRegistrar struct {
	mu      sync.Mutex
	bound   map[string]*events.Bus
	unbound []string
}

func newStubBusRegistrar() *stubBusRegistrar {
	return &stubBusRegistrar{bound: make(map[string]*events.Bus)}
}

func (s *stubBusRegistrar) register(sessionID string, bus *events.Bus) func() {
	s.mu.Lock()
	s.bound[sessionID] = bus
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.bound, sessionID)
			s.unbound = append(s.unbound, sessionID)
			s.mu.Unlock()
		})
	}
}

func (s *stubBusRegistrar) isBound(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.bound[sessionID]
	return ok
}

func (s *stubBusRegistrar) boundCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bound)
}

// withStubBusRegistrar installs a fresh stubBusRegistrar as
// uiadapter.SessionBusRegistrar for the test and restores the previous
// value (nil, in every other test in this package) on cleanup.
func withStubBusRegistrar(t *testing.T) *stubBusRegistrar {
	t.Helper()
	prev := uiadapter.SessionBusRegistrar
	stub := newStubBusRegistrar()
	uiadapter.SessionBusRegistrar = stub.register
	t.Cleanup(func() { uiadapter.SessionBusRegistrar = prev })
	return stub
}

// TestSessionPool_BusWiring_TwoSessionsIndependent proves the pool binds
// two DIFFERENT pooled sessions' buses under their own ids with no
// crosstalk (distinct buses, each bound under its own session id), and
// ReleaseLeases drains both bindings.
func TestSessionPool_BusWiring_TwoSessionsIndependent(t *testing.T) {
	var mu sync.Mutex
	var createdIDs []string
	sessionEvents := make(map[string][]chatsync.EventItem)
	srv := setupSyncMockServer(&mu, &createdIDs, sessionEvents)
	defer srv.Close()

	installTestAuthToken(t)
	stub := withStubBusRegistrar(t)

	res := &config.Resolved{
		Model: "test-model",
		Sync: config.ResolvedSync{
			APIURL: srv.URL, PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 100,
		},
	}

	sess1 := chat.NewSession(res, nil)
	sess1.SessionID = "bus-wiring-1"
	bus1 := events.New()
	sess1.EventBus = bus1

	pool := uiadapter.NewSessionPool(sess1, res, &cliagents.AgentSessionState{WorkspaceRoot: t.TempDir()}, false)

	conv2, err := pool.CreateFresh()
	if err != nil || conv2 == nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	// CreateFresh's new session shares sess1's EventBus (session_pool.go
	// inherits it from the first existing pool member), so both pooled
	// sessions register under the SAME bus pointer but DIFFERENT session
	// ids - the id, not the bus pointer, is what routing keys on.
	time.Sleep(50 * time.Millisecond)

	if !stub.isBound("bus-wiring-1") {
		t.Error("primary session's bus was never registered")
	}
	if stub.boundCount() < 2 {
		t.Errorf("bound count = %d, want at least 2 (one per pooled session)", stub.boundCount())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.ReleaseLeases(ctx)

	if stub.boundCount() != 0 {
		t.Errorf("bound count after ReleaseLeases = %d, want 0 (all bindings drained)", stub.boundCount())
	}
}

// TestSessionPool_BusWiring_NilRegistrarIsNoOp proves a nil
// SessionBusRegistrar (the default in every test and in any build that
// never wires internal/newtui) never panics and simply produces no
// bindings - mirroring SubagentProgressRegistrar's own nil-is-no-op
// contract.
func TestSessionPool_BusWiring_NilRegistrarIsNoOp(t *testing.T) {
	var mu sync.Mutex
	var createdIDs []string
	sessionEvents := make(map[string][]chatsync.EventItem)
	srv := setupSyncMockServer(&mu, &createdIDs, sessionEvents)
	defer srv.Close()

	installTestAuthToken(t)
	prev := uiadapter.SessionBusRegistrar
	uiadapter.SessionBusRegistrar = nil
	t.Cleanup(func() { uiadapter.SessionBusRegistrar = prev })

	res := &config.Resolved{
		Model: "test-model",
		Sync: config.ResolvedSync{
			APIURL: srv.URL, PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 100,
		},
	}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "bus-wiring-niltest"
	sess.EventBus = events.New()

	// Must not panic.
	pool := uiadapter.NewSessionPool(sess, res, &cliagents.AgentSessionState{WorkspaceRoot: t.TempDir()}, false)
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.ReleaseLeases(ctx) // must also not panic
}

// TestSessionPool_ReattachSyncAfterLogin_ClosesTheLoginGap is the
// login-after-session-start test the plan requires: a session created
// while Sync.Active()==false gets no chat-sync session; after
// ReattachSyncAfterLogin runs (simulating a successful /login), that same
// session gains a real chat-sync session; a session already syncing before
// the call gains NO duplicate (attachSyncLocked's own per-id guard).
//
// Mutation proof: making ReattachSyncAfterLogin a no-op (its body
// replaced with a bare `return`) makes this test fail, because the
// pre-login session never gets its post-login sync session and
// createdIDs stays at the pre-login count.
func TestSessionPool_ReattachSyncAfterLogin_ClosesTheLoginGap(t *testing.T) {
	var mu sync.Mutex
	var createdIDs []string
	sessionEvents := make(map[string][]chatsync.EventItem)
	srv := setupSyncMockServer(&mu, &createdIDs, sessionEvents)
	defer srv.Close()

	// Start LOGGED OUT: no auth token installed. Sync.Active(loggedIn=false)
	// is false, so attachSyncLocked refuses at construction time.
	t.Setenv("HOME", t.TempDir())

	res := &config.Resolved{
		Model: "test-model",
		Sync: config.ResolvedSync{
			APIURL: srv.URL, PollWaitSeconds: 1, HeartbeatSeconds: 1, MaxUnflushed: 100,
		},
	}
	sess1 := chat.NewSession(res, nil)
	sess1.SessionID = "login-gap-1"
	sess1.EventBus = events.New()

	pool := uiadapter.NewSessionPool(sess1, res, &cliagents.AgentSessionState{WorkspaceRoot: t.TempDir()}, false)

	// A second session pooled while still logged out - both must pick up
	// sync after login, and neither must double-attach.
	sess2Conv, err := pool.CreateFresh()
	if err != nil || sess2Conv == nil {
		t.Fatalf("CreateFresh: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	preLoginCreated := len(createdIDs)
	mu.Unlock()
	if preLoginCreated != 0 {
		t.Fatalf("created %d remote sessions before login, want 0", preLoginCreated)
	}

	// Simulate a successful /login: install a real token, exactly what
	// miviaauth.Service.Login persists.
	installTestAuthToken(t)

	pool.ReattachSyncAfterLogin()
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	postLoginCreated := len(createdIDs)
	mu.Unlock()
	if postLoginCreated != 2 {
		t.Fatalf("created %d remote sessions after login, want 2 (one per pooled session, no duplicates)", postLoginCreated)
	}

	// A second ReattachSyncAfterLogin call (e.g. a second /login) must be a
	// no-op for sessions already syncing - attachSyncLocked's own
	// `p.syncSessions[id]` guard.
	pool.ReattachSyncAfterLogin()
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	afterSecondCall := len(createdIDs)
	mu.Unlock()
	if afterSecondCall != postLoginCreated {
		t.Fatalf("a second ReattachSyncAfterLogin created %d more remote sessions, want 0 (already-attached sessions must not duplicate)", afterSecondCall-postLoginCreated)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.ReleaseLeases(ctx)
}
