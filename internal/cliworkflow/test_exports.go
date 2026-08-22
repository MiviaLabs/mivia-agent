package cliworkflow

// test_exports.go exposes engine internals that cli tests (which cannot
// reach unexported members) still pin: idle waits and internal state reads.

import (
	"testing"
	"time"
)

// WaitForSessionEngineIdle blocks until the engine has no active record for
// runID, failing the test after 5 seconds.
func WaitForSessionEngineIdle(t *testing.T, e *sessionWorkflowEngine, runID string) {
	WaitForSessionEngineIdleWithin(t, e, runID, 5*time.Second)
}

// WaitForSessionEngineIdleWithin blocks until the engine has no active
// record for runID, failing the test after the given window.
func WaitForSessionEngineIdleWithin(t *testing.T, e *sessionWorkflowEngine, runID string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
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
