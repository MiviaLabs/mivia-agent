package cli

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// TestSessionActiveRunUseLiveCoordinatorNilCases is a regression test:
// useLiveCoordinator must report used=false (and never invoke use) for a nil
// run, a run with no runner, and a run whose resources already closed - the
// caller falls back to a freshly built coordinator in every one of these
// cases instead of assuming a bare active.runner != nil check proves the
// runner is still safe to use.
func TestSessionActiveRunUseLiveCoordinatorNilCases(t *testing.T) {
	called := func(a *sessionActiveRun) bool {
		invoked := false
		used, err := a.useLiveCoordinator(func(coordinator.Coordinator) error {
			invoked = true
			return nil
		})
		if used {
			t.Fatalf("useLiveCoordinator used = true, want false")
		}
		if err != nil {
			t.Fatalf("useLiveCoordinator err = %v, want nil", err)
		}
		return invoked
	}

	if called(nil) {
		t.Fatal("useLiveCoordinator invoked use on a nil *sessionActiveRun")
	}
	if called(&sessionActiveRun{}) {
		t.Fatal("useLiveCoordinator invoked use on a run with no runner")
	}
	closedRun := &sessionActiveRun{runner: controller.NewCoordinatorRunner(coordinator.New(nil, nil))}
	closedRun.closeGuarded()
	if called(closedRun) {
		t.Fatal("useLiveCoordinator invoked use on a run whose resources already closed")
	}
}

// TestSessionActiveRunUseLiveCoordinatorUsesRunner proves the live path:
// when the run has a runner and is not closed, useLiveCoordinator calls use
// with that runner's coordinator and reports used=true.
func TestSessionActiveRunUseLiveCoordinatorUsesRunner(t *testing.T) {
	coord := coordinator.New(nil, nil)
	active := &sessionActiveRun{runner: controller.NewCoordinatorRunner(coord)}
	var got coordinator.Coordinator
	used, err := active.useLiveCoordinator(func(c coordinator.Coordinator) error {
		got = c
		return nil
	})
	if !used {
		t.Fatal("useLiveCoordinator used = false, want true")
	}
	if err != nil {
		t.Fatalf("useLiveCoordinator err = %v, want nil", err)
	}
	if got != coord {
		t.Fatalf("useLiveCoordinator passed coordinator %p, want %p", got, coord)
	}
}

// TestSessionActiveRunCloseGuardedWaitsForInFlightUse is the core regression
// test: closeGuarded (the run-completion goroutine's teardown) must not
// close resources while a Cancel call is mid-use of the live coordinator via
// useLiveCoordinator. Before this guard existed, Cancel captured
// active.runner directly and reused it after stopActive
// returned - which, because closeFn ran before the done channel closed, was
// always a closed store. This test proves the new guard actually serializes
// the two: closeGuarded blocks until the in-flight useLiveCoordinator call
// releases its read lock.
func TestSessionActiveRunCloseGuardedWaitsForInFlightUse(t *testing.T) {
	active := &sessionActiveRun{
		runner: controller.NewCoordinatorRunner(coordinator.New(nil, nil)),
	}
	var closedFlag atomic.Bool
	active.closeFn = func() { closedFlag.Store(true) }

	inUse := make(chan struct{})
	releaseUse := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = active.useLiveCoordinator(func(coordinator.Coordinator) error {
			close(inUse)
			<-releaseUse
			return nil
		})
	}()

	<-inUse // the live-coordinator use is now in flight, holding the read lock

	closeDone := make(chan struct{})
	go func() {
		active.closeGuarded()
		close(closeDone)
	}()

	select {
	case <-closeDone:
		t.Fatal("closeGuarded returned while useLiveCoordinator was still in flight")
	case <-time.After(50 * time.Millisecond):
		// Expected: closeGuarded is blocked on resourceGuard.Lock().
	}
	if closedFlag.Load() {
		t.Fatal("closeFn ran while useLiveCoordinator was still in flight")
	}

	close(releaseUse)
	wg.Wait()

	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("closeGuarded did not complete after the in-flight use released")
	}
	if !closedFlag.Load() {
		t.Fatal("closeFn did not run after closeGuarded completed")
	}

	used, err := active.useLiveCoordinator(func(coordinator.Coordinator) error { return nil })
	if used {
		t.Fatal("useLiveCoordinator reused the runner after closeGuarded ran")
	}
	if err != nil {
		t.Fatalf("useLiveCoordinator err = %v, want nil", err)
	}
}

// TestSessionActiveRunCloseGuardedNilSafe proves closeGuarded is a no-op on
// a nil *sessionActiveRun, matching every other sessionActiveRun method's
// nil-receiver contract in this file.
func TestSessionActiveRunCloseGuardedNilSafe(t *testing.T) {
	var active *sessionActiveRun
	active.closeGuarded()
}
