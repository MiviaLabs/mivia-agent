package delivery

// A child that exits while a GRANDCHILD still holds the inherited stdout pipe
// leaves Wait blocked on the pipe copy, not on the process. Cancelling the
// context kills only the child, so `git push` handing its pipe to ssh or a
// credential helper, or `gh` to its own subprocess, can outlive every deadline
// the caller set.
//
// That is not a slow delivery: engine_deliver defers both the delivering-map
// delete and the run release, so a Deliver that never returns runs neither.
// The run stays in-flight forever, which makes cancel refuse it, delete refuse
// it, and interrupt skip its fence. Permanently un-cancellable and
// un-deletable, from one hung pipe.
//
// cmd.WaitDelay bounds exactly that gap: once the process is gone (or the
// context ends), Wait waits at most WaitDelay for the I/O to finish, then
// closes the pipes and returns.

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// commandWithGrandchildHoldingPipe exits immediately while leaving a
// long-lived grandchild attached to the same stdout pipe.
func commandWithGrandchildHoldingPipe(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", "sleep 30 & echo started")
}

// The mechanism this fix relies on, pinned against the real os/exec: without
// WaitDelay the read blocks on the grandchild; with it, the call returns.
func TestWaitDelayBoundsAGrandchildHoldingThePipe(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := commandWithGrandchildHoldingPipe(ctx)
	cmd.WaitDelay = deliveryWaitDelay

	done := make(chan error, 1)
	go func() {
		_, err := cmd.CombinedOutput()
		done <- err
	}()

	select {
	case <-done:
		// Returned on its own: WaitDelay closed the inherited pipe.
	case <-time.After(15 * time.Second):
		t.Fatal("CombinedOutput blocked on a grandchild holding the pipe; WaitDelay did not bound it")
	}
}

// The bound must be a real positive duration: zero means "wait forever", which
// is the defect this guards.
func TestDeliveryWaitDelayIsPositive(t *testing.T) {
	if deliveryWaitDelay <= 0 {
		t.Fatalf("deliveryWaitDelay = %v; zero disables the bound entirely", deliveryWaitDelay)
	}
}
