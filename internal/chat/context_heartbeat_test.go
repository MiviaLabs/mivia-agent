package chat

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// heartbeatFakeStore is a minimal contextstate.Store + SessionLeaseRenewer
// double that records every RenewLease call, for asserting the ticking
// goroutine's behavior without a real SQLite store.
type heartbeatFakeStore struct {
	mu            sync.Mutex
	renewCount    int
	lastPrincipal contextstate.Principal
	renewErr      error
	releaseCount  int
	releasePrinc  contextstate.Principal
	releaseErr    error
}

func (s *heartbeatFakeStore) EnsureSession(context.Context, contextstate.EnsureSessionRequest) error {
	return nil
}
func (s *heartbeatFakeStore) Commit(context.Context, contextstate.CommitRequest) error   { return nil }
func (s *heartbeatFakeStore) Advance(context.Context, contextstate.AdvanceRequest) error { return nil }
func (s *heartbeatFakeStore) Load(context.Context, contextstate.Principal, string) (contextstate.Snapshot, error) {
	return contextstate.Snapshot{}, nil
}

func (s *heartbeatFakeStore) RenewLease(_ context.Context, principal contextstate.Principal, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewCount++
	s.lastPrincipal = principal
	return s.renewErr
}

func (s *heartbeatFakeStore) ReleaseLease(_ context.Context, principal contextstate.Principal, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseCount++
	s.releasePrinc = principal
	return s.releaseErr
}

func (s *heartbeatFakeStore) counts() (int, contextstate.Principal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewCount, s.lastPrincipal
}

func (s *heartbeatFakeStore) releases() (int, contextstate.Principal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseCount, s.releasePrinc
}

var (
	_ contextstate.Store               = (*heartbeatFakeStore)(nil)
	_ contextstate.SessionLeaseRenewer = (*heartbeatFakeStore)(nil)
)

func heartbeatTestPrincipal(t *testing.T, sessionID, subjectID string) contextstate.Principal {
	t.Helper()
	p, err := contextstate.NewPrincipal("workspace", sessionID, subjectID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// waitForCondition polls cond every 2ms until it reports true or the timeout
// elapses, failing the test on timeout.
func waitForCondition(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !cond() {
		t.Fatal(msg)
	}
}

// TestContextHeartbeat_RepeatedArmSamePrincipalNoDuplicateGoroutine confirms
// arm() called twice with the same store leaves the first ticking goroutine
// running rather than starting a second one. Goroutine-count delta follows
// the same band-based pattern internal/cliorchestrate's
// nested_dispatch_integration_test.go uses (goleak is not vendored in this
// repo).
func TestContextHeartbeat_RepeatedArmSamePrincipalNoDuplicateGoroutine(t *testing.T) {
	store := &heartbeatFakeStore{}
	principal := heartbeatTestPrincipal(t, "sess-1", "subj-1")
	h := newContextHeartbeat(5*time.Millisecond, func() contextstate.Principal { return principal })
	t.Cleanup(h.stop)

	runtime.GC()
	before := runtime.NumGoroutine()

	h.arm(store, principal)
	waitForCondition(t, time.Second, "ticking goroutine never started", func() bool {
		count, _ := store.counts()
		return count > 0
	})
	runtime.GC()
	afterFirstArm := runtime.NumGoroutine()
	if afterFirstArm <= before {
		t.Fatalf("goroutine count = %d after arm, want > %d (before)", afterFirstArm, before)
	}

	// Repeated arm() with the same store must be a no-op on the goroutine
	// count: the existing ticking goroutine is left running.
	h.arm(store, principal)
	h.arm(store, principal)
	time.Sleep(30 * time.Millisecond)
	runtime.GC()
	afterRepeat := runtime.NumGoroutine()
	if afterRepeat > afterFirstArm {
		t.Fatalf("goroutine count grew from %d to %d after repeated arm() calls with the same store", afterFirstArm, afterRepeat)
	}
}

// TestContextHeartbeat_SurvivesPrincipalRotation arms a heartbeat, lets it
// tick under the first principal, then rotates the value the accessor
// closure reads (simulating one of contextPrincipal's mutation sites) and
// asserts the very next tick renews under the NEW principal with no restart
// - the property that lets RotateSessionID/reclaimContextSession stay
// completely uncoordinated with this type.
func TestContextHeartbeat_SurvivesPrincipalRotation(t *testing.T) {
	store := &heartbeatFakeStore{}
	first := heartbeatTestPrincipal(t, "sess-1", "subj-1")
	var current atomic.Value
	current.Store(first)
	h := newContextHeartbeat(5*time.Millisecond, func() contextstate.Principal {
		return current.Load().(contextstate.Principal)
	})
	t.Cleanup(h.stop)

	h.arm(store, first)
	waitForCondition(t, time.Second, "heartbeat never renewed under the first principal", func() bool {
		_, last := store.counts()
		return last == first
	})

	rotated := heartbeatTestPrincipal(t, "sess-2", "subj-1")
	current.Store(rotated)

	waitForCondition(t, time.Second, "heartbeat did not pick up the rotated principal on its own next tick", func() bool {
		_, last := store.counts()
		return last == rotated
	})
}

// TestContextHeartbeat_TeardownRacesCloseAndReclaim races stop() against a
// concurrent rival renewal call on the same store. Run with -race: no panic,
// and no renewal write may land after stop() returns (stop() joins the
// ticking goroutine via its done channel before returning).
func TestContextHeartbeat_TeardownRacesCloseAndReclaim(t *testing.T) {
	store := &heartbeatFakeStore{}
	principal := heartbeatTestPrincipal(t, "sess-1", "subj-1")
	h := newContextHeartbeat(time.Millisecond, func() contextstate.Principal { return principal })
	h.arm(store, principal)
	waitForCondition(t, time.Second, "ticking goroutine never started", func() bool {
		count, _ := store.counts()
		return count > 0
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		h.stop()
	}()
	go func() {
		defer wg.Done()
		// A rival process's own renewal/reclaim path racing teardown - exercised
		// directly against the double store rather than a real SQLite store, to
		// keep this test's failure mode purely about contextHeartbeat's own
		// synchronization (leaked goroutine, panic, post-stop write).
		_ = store.RenewLease(context.Background(), principal, principal.SessionID)
	}()
	wg.Wait()

	countAtStop, _ := store.counts()
	time.Sleep(20 * time.Millisecond)
	countAfter, _ := store.counts()
	if countAfter != countAtStop {
		t.Fatalf("renewCount grew from %d to %d after stop() returned - a renewal landed post-teardown", countAtStop, countAfter)
	}
}

// TestContextHeartbeat_ArmDoesNotDeadlockWhileSessionLockHeld is the F5
// deadlock-regression test: armContextHeartbeat must never synchronously
// re-take s.mu, since both real call sites (SetContextManager,
// SetContextStore) call it after releasing s.mu, and arm() itself must not
// assume the caller has released any lock it might still hold.
func TestContextHeartbeat_ArmDoesNotDeadlockWhileSessionLockHeld(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	store := &heartbeatFakeStore{}
	principal := heartbeatTestPrincipal(t, "sess-1", "subj-1")

	lockHeld := make(chan struct{})
	release := make(chan struct{})
	go func() {
		session.mu.Lock()
		close(lockHeld)
		<-release
		session.mu.Unlock()
	}()
	<-lockHeld
	defer close(release)

	done := make(chan struct{})
	go func() {
		session.armContextHeartbeat(store, principal)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("armContextHeartbeat deadlocked while s.mu was held by another goroutine")
	}
	t.Cleanup(session.contextHeartbeat.stop)
}

// TestSessionReleaseContextLeaseCallsThroughToTheStore is the regression
// test for a real incident: a session whose heartbeat had renewed at least
// once left its lease looking "live" for the rest of sessionLeaseTTL after
// the process quit, so an ordinary quit-then-resume within that window was
// refused as already-live. Session.ReleaseContextLease (wired at
// dispatchChatSurface, the shared exit path for every chat surface) must
// call through to the store's ReleaseLease using the CURRENT principal, and
// must also stop the ticking goroutine so no further renewal re-marks the
// lease fresh after release.
func TestSessionReleaseContextLeaseCallsThroughToTheStore(t *testing.T) {
	store := &heartbeatFakeStore{}
	principal := heartbeatTestPrincipal(t, "sess-1", "subj-1")
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	// Construct directly with a short interval and a closure returning
	// principal, rather than going through armContextHeartbeat (which
	// hardcodes defaultContextHeartbeatInterval, 40s - far too long for a
	// test to wait out a real tick) and session.ContextPrincipal (which
	// would return an unbound zero-value Principal here, since this test
	// never binds a real context store on session).
	session.contextHeartbeatOnce.Do(func() {})
	session.contextHeartbeat = newContextHeartbeat(5*time.Millisecond, func() contextstate.Principal { return principal })
	session.contextHeartbeat.arm(store, principal)
	waitForCondition(t, time.Second, "heartbeat never renewed before release", func() bool {
		count, _ := store.counts()
		return count > 0
	})

	session.ReleaseContextLease(context.Background())

	releaseCount, releasedFor := store.releases()
	if releaseCount != 1 {
		t.Fatalf("ReleaseLease call count = %d, want exactly 1", releaseCount)
	}
	if releasedFor != principal {
		t.Fatalf("ReleaseLease principal = %+v, want %+v", releasedFor, principal)
	}

	// No renewal may land after release: the ticking goroutine must have
	// been stopped, not just outraced once.
	countAtRelease, _ := store.counts()
	time.Sleep(20 * time.Millisecond)
	countAfter, _ := store.counts()
	if countAfter != countAtRelease {
		t.Fatalf("renewCount grew from %d to %d after ReleaseContextLease returned - the heartbeat kept ticking", countAtRelease, countAfter)
	}
}

// TestSessionReleaseContextLeaseIsANoOpWhenNeverArmed confirms a session
// that never bound a context store (no heartbeat ever armed) tolerates
// ReleaseContextLease as a no-op rather than a nil-pointer panic - the
// common case for a plain, context-less chat session.
func TestSessionReleaseContextLeaseIsANoOpWhenNeverArmed(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	session.ReleaseContextLease(context.Background())
}
