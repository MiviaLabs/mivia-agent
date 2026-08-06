package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestDispatchStopsWhenClaimStolen pins the per-batch claim liveness probe:
// when another host force-resumes the run (takes the execution claim) while
// this executor is mid-DAG, the stale executor must STOP dispatching the
// remaining tasks instead of firing every remaining subagent call (whose
// ledger writes are fenced but whose side effects would duplicate).
func TestDispatchStopsWhenClaimStolen(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	theftDone := make(chan struct{})
	var firstCall atomic.Bool
	var secondDispatched atomic.Bool
	_ = d.Register(runtime.Subagent, "worker", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		if firstCall.CompareAndSwap(false, true) {
			close(started)
			// Hold t1 in flight until the test has stolen the claim, so the
			// next batch's liveness probe deterministically runs AFTER the
			// theft (otherwise the worker can beat the test to it).
			<-theftDone
		} else {
			// A second dispatch after the theft is the bug being pinned.
			secondDispatched.Store(true)
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
		{ID: "t2", Name: "worker", DependsOn: []string{"t1"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait until t1 is in flight, then STEAL the run's claim exactly as a
	// force-resume on another host would (clear + claim by a new holder).
	<-started
	if err := repo.ClearRunClaim(context.Background(), h.runID); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(context.Background(), h.runID, "thief-holder"); err != nil {
		t.Fatal(err)
	}
	close(theftDone)

	result, err := c.Join(context.Background(), h)
	if err != nil {
		// The theft surfaces as a run error; what matters is the dispatch stop.
		t.Logf("join error (expected): %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Err == nil || !strings.Contains(result.Err.Error(), "claim was taken") {
		t.Fatalf("run error = %v, want a claim-taken error", result.Err)
	}
	if secondDispatched.Load() {
		t.Fatal("t2 was dispatched after the claim was stolen")
	}
	// Note: whether the stolen run is also left non-terminal depends on the
	// backend's claim fence (storage fenced, memory unfenced); the dispatch
	// stop above is the behavior this test pins.
}
