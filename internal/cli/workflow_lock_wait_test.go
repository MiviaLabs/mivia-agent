package cli

import (
	"testing"
	"time"
)

// testWorkflowResolutionLockWait is the wait the held-lock tests run under. It
// must stay long enough that the bounded acquire really polls and reports the
// "still held after" refusal on a loaded machine, and short enough that a test
// exercising five surfaces against a held lock costs well under a second
// instead of twenty-five.
const testWorkflowResolutionLockWait = 50 * time.Millisecond

// shortenWorkflowResolutionLockWait lowers the bounded execution-lock wait for
// one test and restores it afterwards.
//
// The held-lock tests assert WHICH error a busy flock produces - the explained
// "still held after" refusal rather than the plain acquire's opaque "lock is
// busy" - so the duration is incidental to what they pin. At the production
// five seconds they were pure sleep: one test alone spent 25 seconds waiting
// on five surfaces, and the lock tests together made internal/cli a ~112s
// sequential critical path that `make verify` then paid three times over.
func shortenWorkflowResolutionLockWait(t *testing.T) {
	t.Helper()
	previous := workflowResolutionLockWait
	workflowResolutionLockWait = testWorkflowResolutionLockWait
	t.Cleanup(func() { workflowResolutionLockWait = previous })
}
