package controller

import (
	"context"
	"fmt"
	"testing"
)

// TestAcquireGuardedPanelMemberPermitRejectsCanceledCtx is the regression
// test for Wave 8 audit finding #3: PanelActorLimiter.Acquire races
// l.slots<-struct{}{} against ctx.Done() in one select, and Go's select does
// not prioritize an already-closed ctx.Done() over a simultaneously ready
// send - with an already-canceled ctx and an available slot, the select can
// still pick the slots branch and return a usable lease. Without
// acquireGuardedPanelMemberPermit's explicit post-check, the caller would go
// on to call runnable admission (EnsureMember) on a canceled ctx, violating
// "a canceled permit waiter never calls runnable admission" (required test
// matrix item). This runs many trials with an already-canceled ctx and an
// always-available slot specifically to exercise both arms of that select's
// nondeterministic choice, and asserts every trial is rejected.
func TestAcquireGuardedPanelMemberPermitRejectsCanceledCtx(t *testing.T) {
	limiter := NewPanelActorLimiter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	const trials = 200
	for i := 0; i < trials; i++ {
		runID := fmt.Sprintf("wf-guard-%d", i)
		lease, err := acquireGuardedPanelMemberPermit(ctx, limiter, runID)
		if err == nil {
			t.Fatalf("trial %d: acquireGuardedPanelMemberPermit() err = nil, want ctx.Err() (a canceled permit waiter must never receive a usable lease)", i)
		}
		if lease != nil {
			t.Fatalf("trial %d: acquireGuardedPanelMemberPermit() lease = %v, want nil", i, lease)
		}
	}

	// The limiter's slots must not have leaked across every rejected trial:
	// a live ctx can still fill the full 4-slot capacity afterward.
	live := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := acquireGuardedPanelMemberPermit(live, limiter, fmt.Sprintf("wf-guard-live-%d", i)); err != nil {
			t.Fatalf("live trial %d: acquireGuardedPanelMemberPermit() error = %v, want nil (a prior rejected acquire must have released its slot)", i, err)
		}
	}
}

// TestAcquireGuardedPanelMemberPermitAllowsLiveCtx proves the guard does not
// reject a normal, non-canceled acquire.
func TestAcquireGuardedPanelMemberPermitAllowsLiveCtx(t *testing.T) {
	limiter := NewPanelActorLimiter()
	lease, err := acquireGuardedPanelMemberPermit(context.Background(), limiter, "wf-guard-live")
	if err != nil {
		t.Fatalf("acquireGuardedPanelMemberPermit() error = %v, want nil", err)
	}
	if lease == nil {
		t.Fatal("acquireGuardedPanelMemberPermit() lease = nil, want a granted lease")
	}
	lease.ReleaseBeforeActor()
}
