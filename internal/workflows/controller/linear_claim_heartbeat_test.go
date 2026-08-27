package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// heartbeatTransientRepo fails RefreshRunClaim for the first failures calls
// and succeeds afterwards. It simulates a transient SQLite fault.
type heartbeatTransientRepo struct {
	workflowledger.Repository
	failures int
	calls    atomic.Int32
}

func (r *heartbeatTransientRepo) RefreshRunClaim(context.Context, string, string) error {
	if r.calls.Add(1) <= int32(r.failures) {
		return errors.New("sqlite busy")
	}
	return nil
}

// heartbeatHeldRepo always reports that another holder owns the run.
type heartbeatHeldRepo struct {
	workflowledger.Repository
	calls atomic.Int32
}

func (r *heartbeatHeldRepo) RefreshRunClaim(context.Context, string, string) error {
	r.calls.Add(1)
	return workflowledger.ErrClaimHeld
}

// heartbeatLostRepo reports that the claim row is gone (the run was taken over
// and released by another holder), which a heartbeat must treat as lost.
type heartbeatLostRepo struct {
	workflowledger.Repository
	calls atomic.Int32
}

func (r *heartbeatLostRepo) RefreshRunClaim(context.Context, string, string) error {
	r.calls.Add(1)
	return workflowledger.ErrClaimNotHeld
}

func TestLinearClaimHeartbeatContinuesOnTransientError(t *testing.T) {
	old := claimHeartbeatInterval
	claimHeartbeatInterval = 2 * time.Millisecond
	t.Cleanup(func() { claimHeartbeatInterval = old })

	repo := &heartbeatTransientRepo{failures: 2}
	var canceled atomic.Bool
	ctrl := &LinearController{Repo: repo, RunID: "wfr-transient-heartbeat", Holder: "holder-a"}
	stop := ctrl.startClaimHeartbeat(func() { canceled.Store(true) })
	// Wait until the ticker has run past the transient failures. Poll with a
	// ticker: time.After is allowed by the project's test policy, time.Sleep
	// is not.
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	deadline := time.After(time.Second)
	for repo.calls.Load() <= int32(repo.failures) {
		select {
		case <-deadline:
			t.Fatalf("ClaimRun calls = %d after 1s, want ticks past %d transient failures", repo.calls.Load(), repo.failures)
		case <-poll.C:
		}
	}
	stop()
	if canceled.Load() {
		t.Fatal("transient claim refresh error canceled the step")
	}
	if repo.calls.Load() <= int32(repo.failures) {
		t.Fatalf("ClaimRun calls = %d, want ticks past %d transient failures", repo.calls.Load(), repo.failures)
	}
}

func TestLinearClaimHeartbeatCancelsOnErrClaimHeld(t *testing.T) {
	old := claimHeartbeatInterval
	claimHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { claimHeartbeatInterval = old })

	repo := &heartbeatHeldRepo{}
	assertHeartbeatCancels(t, repo)
}

// TestLinearClaimHeartbeatCancelsOnLostClaim pins that a missing claim row (the
// run was taken over and released by another holder, so RefreshRunClaim returns
// ErrClaimNotHeld) cancels the step: the displaced holder must not keep
// executing concurrently with the new holder.
func TestLinearClaimHeartbeatCancelsOnLostClaim(t *testing.T) {
	old := claimHeartbeatInterval
	claimHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { claimHeartbeatInterval = old })

	repo := &heartbeatLostRepo{}
	assertHeartbeatCancels(t, repo)
}

// TestLinearClaimHeartbeatStopReturnsWhenRefreshBlocks pins that stop() does not
// wait forever if a single RefreshRunClaim call is wedged. Without a bounded
// context the heartbeat goroutine would never return and Advance cleanup would
// deadlock.
func TestLinearClaimHeartbeatStopReturnsWhenRefreshBlocks(t *testing.T) {
	old := claimHeartbeatInterval
	claimHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { claimHeartbeatInterval = old })

	repo := &heartbeatBlockingRepo{block: make(chan struct{}), called: make(chan struct{})}
	ctrl := &LinearController{Repo: repo, RunID: "wfr-block", Holder: "holder-a"}
	stop := ctrl.startClaimHeartbeat(func() {})
	defer close(repo.block)

	// Wait until the heartbeat has actually entered RefreshRunClaim.
	select {
	case <-repo.called:
	case <-time.After(time.Second):
		t.Fatal("heartbeat never entered RefreshRunClaim")
	}

	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * durableHeartbeatTimeout):
		t.Fatal("stop did not return while RefreshRunClaim was blocked")
	}
}

// heartbeatBlockingRepo simulates a store call that respects context
// cancellation. Only the first RefreshRunClaim blocks until the context is
// canceled; later calls return immediately so the test can verify that stop()
// returns after the bounded timeout.
type heartbeatBlockingRepo struct {
	workflowledger.Repository
	block  chan struct{}
	called chan struct{}
	once   sync.Once
	long   sync.Once
}

func (r *heartbeatBlockingRepo) RefreshRunClaim(ctx context.Context, _, _ string) error {
	r.once.Do(func() { close(r.called) })
	r.long.Do(func() {
		select {
		case <-r.block:
		case <-ctx.Done():
		}
	})
	return ctx.Err()
}

func assertHeartbeatCancels(t *testing.T, repo workflowledger.Repository) {
	t.Helper()
	canceled := make(chan struct{}, 1)
	ctrl := &LinearController{Repo: repo, RunID: "wfr-heartbeat-cancel", Holder: "holder-a"}
	stop := ctrl.startClaimHeartbeat(func() {
		select {
		case canceled <- struct{}{}:
		default:
		}
	})
	defer stop()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("cancel did not fire within one interval")
	}
}
