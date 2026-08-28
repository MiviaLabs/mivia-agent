package subagents

import (
	"context"
	"encoding/json"
	stdruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// ---------------------------------------------------------------------------
// Unlimited sentinel tests
// ---------------------------------------------------------------------------
// These tests verify that Policy fields with the Unlimited sentinel (-1)
// disable their respective limits. Zero values now get safe defaults applied
// by New(); use Unlimited to explicitly opt out of bounds.

// instantHandler is a minimal runtime.Handler that returns immediately.
type instantHandler struct{}

func (h *instantHandler) Invoke(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
	return json.RawMessage(`{"output":"done"}`), nil
}

// countingHandler tracks peak concurrency using atomics and uses a barrier
// channel to ensure overlap between concurrent invocations without sleeping.
type countingHandler struct {
	active  *atomic.Int32
	peak    *atomic.Int32
	wg      *sync.WaitGroup
	barrier chan struct{} // closed when all workers have started
}

func (h *countingHandler) Invoke(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
	h.wg.Add(1)
	defer h.wg.Done()

	cur := h.active.Add(1)
	// CAS loop to update peak.
	for {
		p := h.peak.Load()
		if cur <= p || h.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	// Wait until the test signals all workers have started, ensuring
	// concurrency is observed deterministically without time.Sleep.
	<-h.barrier
	h.active.Add(-1)
	return json.RawMessage(`{"output":"done"}`), nil
}

// TestPoolUnlimitedWorkersDispatchesAll verifies that Workers: 0 allows all
// tasks to run concurrently rather than capping at a default worker count.
// With 8 tasks each sleeping 50 ms, peak concurrency must exceed 4.
func TestPoolUnlimitedWorkersDispatchesAll(t *testing.T) {
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	barrier := make(chan struct{})

	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "instant", &countingHandler{
		active:  &active,
		peak:    &peak,
		wg:      &wg,
		barrier: barrier,
	}); err != nil {
		t.Fatal(err)
	}

	p := New(d, Policy{Workers: 0})

	tasks := make([]Task, 8)
	for i := range tasks {
		tasks[i] = Task{
			ID:     "t" + string(rune('0'+i)),
			Name:   "instant",
			Input:  json.RawMessage(`"go"`),
			Budget: 1,
		}
	}

	// Run in background; close the barrier once all 8 workers are active.
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = p.Run(context.Background(), tasks)
	}()

	// Poll until all 8 tasks are active, then release them.
	for !t.Failed() {
		if active.Load() >= 8 {
			close(barrier)
			break
		}
		// Yield to let workers start.
		stdruntime.Gosched()
		select {
		case <-runDone:
			// Run finished before we hit 8 concurrent - try again with timing.
		default:
		}
	}

	<-runDone
	wg.Wait()

	observed := peak.Load()
	t.Logf("peak concurrency: %d", observed)
	if observed <= 4 {
		t.Fatalf("peak concurrency = %d, expected > 4 (Workers: 0 should not be capped)", observed)
	}
}

// TestPoolUnlimitedFanoutAcceptsAll verifies that MaxFanout: Unlimited disables
// the fan-out limit so the pool accepts arbitrarily many tasks without
// returning "fan-out limit exceeded".
func TestPoolUnlimitedFanoutAcceptsAll(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "instant", &instantHandler{}); err != nil {
		t.Fatal(err)
	}

	p := New(d, Policy{Workers: 0, MaxFanout: Unlimited})

	tasks := make([]Task, 20)
	for i := range tasks {
		tasks[i] = Task{
			ID:     "f" + string(rune('0'+i%10)) + string(rune('0'+i/10)),
			Name:   "instant",
			Input:  json.RawMessage(`"go"`),
			Budget: 1,
		}
	}

	results, err := p.Run(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if len(results) != 20 {
		t.Fatalf("expected 20 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("task %s failed: %v", r.TaskID, r.Err)
		}
	}
}

// TestPoolUnlimitedDepthSkipsCheck verifies that MaxDepth: Unlimited disables
// the depth limit so the pool accepts tasks with arbitrarily large Depth
// without returning "depth limit exceeded". The dispatcher's own MaxDepth is
// set high enough to avoid a secondary rejection.
func TestPoolUnlimitedDepthSkipsCheck(t *testing.T) {
	// Set dispatcher MaxDepth high enough so it does not reject Depth=100.
	d := runtime.New(runtime.Policy{MaxDepth: 200})
	if err := d.Register(runtime.Subagent, "instant", &instantHandler{}); err != nil {
		t.Fatal(err)
	}

	p := New(d, Policy{Workers: 1, MaxDepth: Unlimited})

	tasks := []Task{{
		ID:     "deep1",
		Name:   "instant",
		Input:  json.RawMessage(`"go"`),
		Budget: 1,
		Depth:  100,
	}}

	results, err := p.Run(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Err != nil {
		t.Fatalf("task deep1 failed: %v", results[0].Err)
	}
}

// TestPolicySafeDefaultsApplied verifies that zero-valued Policy fields get
// safe non-zero defaults, so a zero from missing config does not mean unlimited.
func TestPolicySafeDefaultsApplied(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	p := New(d, Policy{})
	if p.p.MaxFanout != DefaultMaxFanout {
		t.Fatalf("MaxFanout: got %d, want %d (safe default)", p.p.MaxFanout, DefaultMaxFanout)
	}
	if p.p.MaxDepth != DefaultMaxDepth {
		t.Fatalf("MaxDepth: got %d, want %d (safe default)", p.p.MaxDepth, DefaultMaxDepth)
	}
	if p.p.MaxBudget != DefaultMaxBudget {
		t.Fatalf("MaxBudget: got %d, want %d (safe default)", p.p.MaxBudget, DefaultMaxBudget)
	}
}

// TestPolicyUnlimitedSentinelDisablesBounds verifies that Unlimited (-1)
// disables bounds even after defaults would otherwise apply.
func TestPolicyUnlimitedSentinelDisablesBounds(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	p := New(d, Policy{MaxFanout: Unlimited, MaxDepth: Unlimited, MaxBudget: Unlimited})
	if p.p.MaxFanout != Unlimited {
		t.Fatalf("MaxFanout: got %d, want %d (unlimited sentinel)", p.p.MaxFanout, Unlimited)
	}
	if p.p.MaxDepth != Unlimited {
		t.Fatalf("MaxDepth: got %d, want %d (unlimited sentinel)", p.p.MaxDepth, Unlimited)
	}
	if p.p.MaxBudget != Unlimited {
		t.Fatalf("MaxBudget: got %d, want %d (unlimited sentinel)", p.p.MaxBudget, Unlimited)
	}
}

// TestDefaultBudgetAdmitsARealisticDispatchBatch is the regression for a
// real user-reported incident: dispatch_tasks batches immediately failed
// every task with "budget limit exceeded"/"run budget exceeded" and a
// terminationReason of never_started. Root cause: DefaultMaxBudget (the
// safe-default MaxBudget an unconfigured [subagents] default_budget=0
// resolves to, mirroring MaxFanout/MaxDepth's "0 means safe default, not
// unlimited" policy) was 1000 - far below what a single realistic
// dispatch_tasks task requests in practice (thousands per task is normal;
// this pins 4 tasks at 6000 each, exactly the batch shape from the
// incident report), so ordinary usage tripped the "safety" cap on the
// very first call. validate must accept a realistic batch under the
// unconfigured (zero-value Policy) default.
func TestDefaultBudgetAdmitsARealisticDispatchBatch(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	p := New(d, Policy{})
	if err := d.Register(runtime.Subagent, "instant", &instantHandler{}); err != nil {
		t.Fatal(err)
	}
	tasks := make([]Task, 4)
	for i := range tasks {
		tasks[i] = Task{ID: taskIDs4[i], Name: "instant", Input: json.RawMessage(`{}`), Budget: 6000}
	}
	results, err := p.Run(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Run() err = %v, want nil (a realistic budget must not trip the unconfigured default cap)", err)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("task %s err = %v, want nil", r.TaskID, r.Err)
		}
	}
}

var taskIDs4 = [4]string{"explore-meta", "explore-app-architecture", "explore-data-layer", "explore-testing-gates"}
