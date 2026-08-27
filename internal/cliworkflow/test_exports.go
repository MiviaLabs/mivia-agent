package cliworkflow

// test_exports.go exposes engine internals that cli tests (which cannot
// reach unexported members) still pin: idle waits and internal state reads.

import (
	"runtime"
	"testing"
	"time"
)

// ciDeadline scales a wall-clock test budget for the platform it runs on:
// windows-latest CI runners execute the real SQLite+WAL and git subprocess
// steps in these suites an order of magnitude slower than the Linux baseline
// the constants were measured on. Non-Windows budgets stay tight.
func ciDeadline(d time.Duration) time.Duration {
	if runtime.GOOS == "windows" {
		return 3 * d
	}
	return d
}

// WaitForSessionEngineIdle blocks until the engine has no active record for
// runID, failing the test after 5 seconds.
func WaitForSessionEngineIdle(t *testing.T, e *sessionWorkflowEngine, runID string) {
	WaitForSessionEngineIdleWithin(t, e, runID, ciDeadline(5*time.Second))
}

// WaitForSessionEngineIdleWithin blocks until the engine has no active
// record for runID, failing the test after the given window.
func WaitForSessionEngineIdleWithin(t *testing.T, e *sessionWorkflowEngine, runID string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(ciDeadline(within))
	for time.Now().Before(deadline) {
		e.mu.Lock()
		active, ok := e.active[runID]
		e.mu.Unlock()
		if !ok {
			return
		}
		select {
		case <-active.done:
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("engine did not finish run %s within deadline", runID)
}

// testWorkflowResolutionLockWait is the wait the held-lock tests run under.
const testWorkflowResolutionLockWait = 50 * time.Millisecond

// ShortenWorkflowResolutionLockWaitForTest shortens the resolution lock wait
// for the calling test and restores it on cleanup.
func ShortenWorkflowResolutionLockWaitForTest(t *testing.T) {
	t.Helper()
	previous := WorkflowResolutionLockWait
	WorkflowResolutionLockWait = testWorkflowResolutionLockWait
	t.Cleanup(func() { WorkflowResolutionLockWait = previous })
}
