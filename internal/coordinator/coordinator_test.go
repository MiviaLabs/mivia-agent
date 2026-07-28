package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// staticHandler returns a fixed response for any invocation.
type staticHandler struct {
	out json.RawMessage
	err error
}

func (h staticHandler) Invoke(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
	return h.out, h.err
}

// slowHandler blocks until context is done, then returns ctx.Err().
type slowHandler struct{}

func (slowHandler) Invoke(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// invoker is a function-based runtime.Handler for tests.
type invoker func(context.Context, runtime.Request) (json.RawMessage, error)

func (f invoker) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return f(ctx, req)
}

func TestCoordinator_SpawnReturnsHandle(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle")
	}
	if h.runID == "" {
		t.Fatal("expected non-empty run ID")
	}
}

func TestCoordinator_SpawnIdempotency(t *testing.T) {
	// MUTATION PROOF 3: Duplicate Spawn with same IdempotencyKey returns
	// the existing RunHandle without creating a new run.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h1, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "key-1")
	if err != nil {
		t.Fatal(err)
	}

	h2, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "key-1")
	if err != nil {
		t.Fatal(err)
	}

	if h1 != h2 {
		t.Fatal("MUTATION FAIL: duplicate Spawn with same idempotency key returned different handles")
	}
	if h1.runID != h2.runID {
		t.Fatal("handles have different run IDs")
	}
}

func TestCoordinator_SpawnRejectsEmptyTaskList(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	p := subagents.New(d, subagents.Policy{})
	c := New(repo, p)

	_, err := c.Spawn(context.Background(), nil, "")
	if err == nil {
		t.Fatal("expected error for empty task list")
	}
}

func TestCoordinator_Inspect(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.RunID != h.runID {
		t.Fatalf("expected run ID %q, got %q", h.runID, snap.RunID)
	}
	if len(snap.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap.Tasks))
	}
}

func TestCoordinator_Join(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
}

func TestCoordinator_Cancel(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "slow"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for task to start before canceling.
	<-started

	if err := c.Cancel(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	// Join should succeed after cancel
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestCoordinator_JoinContextCancellation(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "slow", slowHandler{})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "slow"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = c.Join(ctx, h)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestCoordinator_DependencyBlocking(t *testing.T) {
	// MUTATION PROOF 5: Blocked dependencies produce blocked status, not
	// completed, for tasks whose dependencies fail.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fail", staticHandler{err: errors.New("intentional failure")})
	_ = d.Register(runtime.Subagent, "child", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "parent", Name: "fail"},
		{ID: "child", Name: "child", DependsOn: []string{"parent"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	// Check child was blocked
	for _, r := range result.Results {
		if r.TaskID == "child" && r.Status != "blocked" {
			t.Fatalf("MUTATION FAIL: child status=%q, want 'blocked'", r.Status)
		}
	}
}

func TestCoordinator_DisplayNameUniqueness(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h1, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	h2, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t2", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	snap1, _ := c.Inspect(context.Background(), h1)
	snap2, _ := c.Inspect(context.Background(), h2)

	if snap1.DisplayName == snap2.DisplayName {
		t.Fatal("display names should be unique across runs")
	}
}

func TestCoordinator_RedactedOutput(t *testing.T) {
	// MUTATION PROOF 4: Redaction enforcement — output stored in the ledger
	// is a bounded reference, not raw content.
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"secret":"data"}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "test"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	task, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	// OutputRef should be a bounded reference (length prefix), not raw content
	if task.OutputRef == "" {
		t.Fatal("expected non-empty output ref")
	}
	if task.OutputRef == `{"secret":"data"}` {
		t.Fatal("MUTATION FAIL: raw output stored in ledger, expected bounded reference")
	}
	if task.OutputRef != "output:16" {
		t.Logf("output ref: %q", task.OutputRef)
	}
}

func TestCoordinator_ConcurrentSpawn(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "test", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 2, Partial: true})
	c := New(repo, p)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("t%d", i)
			h, err := c.Spawn(context.Background(), []subagents.Task{
				{ID: id, Name: "test"},
			}, "")
			if err != nil {
				t.Errorf("spawn failed: %v", err)
				return
			}
			_, err = c.Join(context.Background(), h)
			if err != nil {
				t.Errorf("join failed: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func TestCoordinator_ValidateTasksRejectsUnknownDependency(t *testing.T) {
	c := &Coordinator{}
	err := c.validateTasks([]subagents.Task{
		{ID: "t1", DependsOn: []string{"nonexistent"}},
	})
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
}

func TestCoordinator_ValidateTasksRejectsDuplicateID(t *testing.T) {
	c := &Coordinator{}
	err := c.validateTasks([]subagents.Task{
		{ID: "t1"},
		{ID: "t1"},
	})
	if err == nil {
		t.Fatal("expected error for duplicate task ID")
	}
}
