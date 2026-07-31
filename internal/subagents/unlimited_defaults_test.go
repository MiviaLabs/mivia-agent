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
// Zero-means-unlimited integration tests
// ---------------------------------------------------------------------------
// These tests verify that Policy fields with value 0 disable their respective
// limits rather than capping at a default (e.g. Workers=0 should allow all
// tasks to run concurrently, MaxFanout=0 should accept any number of tasks,
// MaxDepth=0 should skip depth validation).

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
			// Run finished before we hit 8 concurrent — try again with timing.
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

// TestPoolUnlimitedFanoutAcceptsAll verifies that MaxFanout: 0 disables the
// fan-out limit so the pool accepts arbitrarily many tasks without returning
// "fan-out limit exceeded".
func TestPoolUnlimitedFanoutAcceptsAll(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "instant", &instantHandler{}); err != nil {
		t.Fatal(err)
	}

	p := New(d, Policy{Workers: 0, MaxFanout: 0})

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

// TestPoolUnlimitedDepthSkipsCheck verifies that MaxDepth: 0 disables the
// depth limit so the pool accepts tasks with arbitrarily large Depth without
// returning "depth limit exceeded". The dispatcher's own MaxDepth is set
// high enough to avoid a secondary rejection.
func TestPoolUnlimitedDepthSkipsCheck(t *testing.T) {
	// Set dispatcher MaxDepth high enough so it does not reject Depth=100.
	d := runtime.New(runtime.Policy{MaxDepth: 200})
	if err := d.Register(runtime.Subagent, "instant", &instantHandler{}); err != nil {
		t.Fatal(err)
	}

	p := New(d, Policy{Workers: 1, MaxDepth: 0})

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
