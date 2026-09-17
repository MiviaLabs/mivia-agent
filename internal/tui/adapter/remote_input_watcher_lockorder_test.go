package adapter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestRemoteInputWatcher_BackfillCallsIsPooledWithoutHoldingWatcherLock pins the
// lock order between the watcher's own mutex and the pool's. IsPooled is
// StartBackgroundWatch's closure over SessionPool.mu (session_pool_sync.go), and
// every attach path holds that same pool lock while calling this watcher's
// StopSync -> w.mu (attachSyncLocked). Calling IsPooled under w.mu therefore
// inverts the order - pool.mu -> w.mu on the attach side, w.mu -> pool.mu in
// Backfill - and both sides block forever: neither sync.Mutex acquisition has a
// timeout, and StopSync's 2s budget is only spent after it already owns w.mu.
//
// TryLock is the probe rather than a timing window: from inside IsPooled it
// reports whether the caller holds w.mu, which is exactly what a non-reentrant
// mutex refuses to a goroutine that already owns it. candidates() legitimately
// calls IsPooled unlocked, so the counter also proves the call happened at all -
// a probe that never runs proves nothing.
func TestRemoteInputWatcher_BackfillCallsIsPooledWithoutHoldingWatcherLock(t *testing.T) {
	sess, wsRoot := newBackfillFixture(t)
	server := newMockInputServer(t)
	defer server.Close()

	var w *RemoteInputWatcher
	var calls, held int64
	cfg := WatcherConfig{
		Seed: sess, Tokens: func(context.Context, bool) (string, error) { return "test-token", nil },
		Res: backfillTestRes(server.URL), WorkspaceRoot: wsRoot, Max: 8,
		AuthorProvider: func(context.Context) (string, error) { return "auth-1", nil },
		IsPooled: func(string) bool {
			atomic.AddInt64(&calls, 1)
			if w.mu.TryLock() {
				w.mu.Unlock()
				return false
			}
			atomic.AddInt64(&held, 1)
			return false
		},
	}
	w = NewRemoteInputWatcher(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Backfill(ctx)

	w.mu.Lock()
	_, watching := w.watching["cand-1"]
	w.mu.Unlock()
	w.Stop(ctx)

	if got := atomic.LoadInt64(&calls); got == 0 {
		t.Fatal("IsPooled was never called; the probe proved nothing")
	}
	if got := atomic.LoadInt64(&held); got != 0 {
		t.Fatalf("Backfill called IsPooled %d time(s) while holding w.mu; attach holds pool.mu and takes w.mu via StopSync, so this order deadlocks", got)
	}
	if !watching {
		t.Fatal("Backfill did not start a poller for the real unpooled candidate")
	}
}

// TestRemoteInputWatcher_BackfillDropsCandidatesPooledSinceEnumeration pins the
// freshness pass itself: candidates() keeps the candidate, then the pre-lock
// re-read reports it pooled, so no poller is installed for a session that
// became a pool member between the enumeration and the install. The counter
// proves the second (post-enumeration) call happened - a single call would mean
// only candidates() filtered it and this pass never ran.
func TestRemoteInputWatcher_BackfillDropsCandidatesPooledSinceEnumeration(t *testing.T) {
	sess, wsRoot := newBackfillFixture(t)
	server := newMockInputServer(t)
	defer server.Close()

	var w *RemoteInputWatcher
	var candCalls int
	cfg := WatcherConfig{
		Seed: sess, Tokens: func(context.Context, bool) (string, error) { return "test-token", nil },
		Res: backfillTestRes(server.URL), WorkspaceRoot: wsRoot, Max: 8,
		AuthorProvider: func(context.Context) (string, error) { return "auth-1", nil },
		IsPooled: func(id string) bool {
			if id != "cand-1" {
				return false
			}
			candCalls++
			return candCalls > 1
		},
	}
	w = NewRemoteInputWatcher(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Backfill(ctx)

	w.mu.Lock()
	watchCount := len(w.watching)
	w.mu.Unlock()
	if candCalls != 2 {
		t.Fatalf("IsPooled(cand-1) called %d time(s), want 2 (enumeration + freshness pass)", candCalls)
	}
	if watchCount != 0 {
		t.Fatalf("watching count = %d, want 0: a candidate pooled since the enumeration must not be watched", watchCount)
	}
}

// TestSessionPool_AttachUnderPoolLockDoesNotDeadlockBackfill drives the
// interleaving the two locks must survive, and fails on a timeout instead of
// hanging the suite: a Backfill enumeration blocked inside IsPooled (the closure
// StartBackgroundWatch installs, over pool.mu) while an attach holds pool.mu and
// reaches attachSyncLocked's StopSync -> w.mu. IsPooled is held at the SECOND
// call for the candidate - candidates() calls it unlocked, so the second call is
// the freshness re-check that must not run under w.mu. The attach is released
// only after it provably owns pool.mu, so the interleaving is forced, not raced.
//
// The attach path is left at a zero Sync config on purpose: StopSync (the w.mu
// acquisition) runs before attachSyncLocked's sync-active check, and the locks
// under test do not depend on a logged-in session.
func TestSessionPool_AttachUnderPoolLockDoesNotDeadlockBackfill(t *testing.T) {
	sess, wsRoot := newBackfillFixture(t)
	server := newMockInputServer(t)
	defer server.Close()

	res := &config.Resolved{Sync: config.ResolvedSync{APIURL: server.URL, PollWaitSeconds: 1, BackgroundWatchMax: 8}}
	pool := &SessionPool{res: res, sessions: map[string]*chat.Session{}}
	attachSess := chat.NewSession(res, nil)
	attachSess.SessionID = "sess-attach"
	attachSess.EventBus = events.New()

	const candidateID = "cand-1"
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	var seenCand int

	w := NewRemoteInputWatcher(WatcherConfig{
		Seed: sess, Tokens: func(context.Context, bool) (string, error) { return "test-token", nil },
		Res: res, WorkspaceRoot: wsRoot, Max: 8,
		AuthorProvider: func(context.Context) (string, error) { return "auth-1", nil },
		IsPooled: func(id string) bool {
			if id == candidateID {
				seenCand++
				if seenCand == 2 {
					enterOnce.Do(func() { close(entered) })
					<-release
				}
			}
			pool.mu.Lock()
			_, pooled := pool.sessions[id]
			pool.mu.Unlock()
			return pooled
		},
	})
	pool.watcher = w

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backfillDone := make(chan struct{})
	go func() {
		w.Backfill(ctx)
		close(backfillDone)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Backfill never reached the candidate's second IsPooled call")
	}

	attachHoldsPoolLock := make(chan struct{})
	attachDone := make(chan struct{})
	go func() {
		pool.mu.Lock()
		close(attachHoldsPoolLock)
		pool.attachSyncLocked(attachSess)
		pool.mu.Unlock()
		close(attachDone)
	}()

	<-attachHoldsPoolLock
	close(release)

	select {
	case <-attachDone:
	case <-time.After(3 * time.Second):
		t.Fatal("attach holding pool.mu never returned: it waits for w.mu while Backfill's IsPooled waits for pool.mu (lock-order inversion)")
	}
	select {
	case <-backfillDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Backfill never returned after IsPooled was released")
	}
	w.Stop(ctx)
}
