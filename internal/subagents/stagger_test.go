package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// staggerRecorder captures the wall-clock start of every handler invocation.
type staggerRecorder struct {
	mu     sync.Mutex
	starts map[string]time.Time
	delay  time.Duration
}

func newStaggerRecorder(delay time.Duration) *staggerRecorder {
	return &staggerRecorder{starts: map[string]time.Time{}, delay: delay}
}

func (r *staggerRecorder) handler() handlerFunc {
	return func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		r.mu.Lock()
		r.starts[req.Name] = time.Now()
		r.mu.Unlock()
		if r.delay > 0 {
			select {
			case <-time.After(r.delay):
			case <-ctx.Done():
			}
		}
		return json.RawMessage(`{"done":true}`), nil
	}
}

func (r *staggerRecorder) snapshot() map[string]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]time.Time, len(r.starts))
	for k, v := range r.starts {
		out[k] = v
	}
	return out
}

// TestExecuteStaggersConcurrentStarts pins the anti-thundering-herd behavior:
// with SpawnStagger set and unlimited workers, consecutive task starts must be
// separated by roughly the stagger interval instead of all firing at once.
// The step-1 provider-overload hang class lands here.
func TestExecuteStaggersConcurrentStarts(t *testing.T) {
	const stagger = 40 * time.Millisecond
	rec := newStaggerRecorder(10 * time.Millisecond)
	d := runtime.New(runtime.Policy{})
	for _, name := range []string{"t1", "t2", "t3"} {
		_ = d.Register(runtime.Subagent, name, rec.handler())
	}
	p := New(d, Policy{Workers: 0, SpawnStagger: stagger})
	tasks := []Task{{ID: "t1", Name: "t1"}, {ID: "t2", Name: "t2"}, {ID: "t3", Name: "t3"}}
	if _, err := p.Run(context.Background(), tasks); err != nil {
		t.Fatal(err)
	}
	got := rec.snapshot()
	if len(got) != len(tasks) {
		t.Fatalf("handler starts = %v, want %d entries", got, len(tasks))
	}
	first, ok := got["t1"]
	if !ok {
		t.Fatal("task t1 never started")
	}
	for _, id := range []string{"t2", "t3"} {
		start, ok := got[id]
		if !ok {
			t.Fatalf("task %q never started", id)
		}
		if gap := start.Sub(first); gap < stagger/2 {
			t.Errorf("task %q started %s after t1; want >= ~%s (stagger not applied)", id, gap, stagger/2)
		}
	}
}

// TestExecuteZeroStaggerStartsTogether guards against the stagger path adding
// latency when disabled: with SpawnStagger 0, concurrent tasks must start
// within one scheduling quantum of each other.
func TestExecuteZeroStaggerStartsTogether(t *testing.T) {
	rec := newStaggerRecorder(0)
	d := runtime.New(runtime.Policy{})
	for _, name := range []string{"t1", "t2"} {
		_ = d.Register(runtime.Subagent, name, rec.handler())
	}
	p := New(d, Policy{Workers: 0})
	tasks := []Task{{ID: "t1", Name: "t1"}, {ID: "t2", Name: "t2"}}
	if _, err := p.Run(context.Background(), tasks); err != nil {
		t.Fatal(err)
	}
	got := rec.snapshot()
	a, b := got["t1"], got["t2"]
	if a.IsZero() || b.IsZero() {
		t.Fatalf("missing starts: %v", got)
	}
	if gap := a.Sub(b); gap > 25*time.Millisecond && gap < -25*time.Millisecond {
		t.Errorf("unconfigured stagger spread = %s; want near-simultaneous start", gap)
	}
}

// TestExecuteCancelDuringStaggerGapRecordsCanceled covers the ctx.Done branch
// added alongside the stagger wait: a task whose feed is still waiting out its
// gap when the caller cancels must come back as canceled, not hang the pool.
func TestExecuteCancelDuringStaggerGapRecordsCanceled(t *testing.T) {
	block := make(chan struct{})
	defer close(block) // release any handler still parked when assertions end
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "first", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		select { // hold its slot past the cancel
		case <-block:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	}))
	_ = d.Register(runtime.Subagent, "in-gap", h{})
	p := New(d, Policy{Workers: 0, SpawnStagger: 250 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var res []Result
	var runErr error
	go func() {
		res, runErr = p.Run(ctx, []Task{{ID: "first", Name: "first"}, {ID: "in-gap", Name: "in-gap"}})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // land inside the in-gap task's stagger wait
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pool.Run did not return after cancel during a stagger gap")
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run err = %v, want nil or context.Canceled", runErr)
	}
	for _, r := range res {
		if r.TaskID == "in-gap" && r.Status != "canceled" {
			t.Fatalf("in-gap status = %q, want canceled", r.Status)
		}
	}
}
