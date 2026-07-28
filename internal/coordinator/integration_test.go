package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// integration tests for the coordinator + memory-ledger + subagent pool

func TestIntegration_SingleTaskCompletes(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	res, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if res.Err != nil {
		t.Fatalf("unexpected err: %v", res.Err)
	}

	// Verify ledger state via Inspect
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != ledger.RunStatusCompleted {
		t.Fatalf("run status = %q, want %q", snap.Status, ledger.RunStatusCompleted)
	}
	if len(snap.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap.Tasks))
	}
	if snap.Tasks[0].Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("task status = %q, want %q", snap.Tasks[0].Status, ledger.TaskStatusCompleted)
	}
	if snap.Tasks[0].OutputRef == "" {
		t.Fatal("expected non-empty output ref")
	}
	if snap.CompletedAt == nil {
		t.Fatal("expected completed time")
	}
}

func TestIntegration_MultiTaskDAG(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 2})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "a", Name: "worker"},
		{ID: "b", Name: "worker", DependsOn: []string{"a"}},
		{ID: "c", Name: "worker", DependsOn: []string{"a"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	res, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if res.Err != nil {
		t.Fatalf("unexpected err: %v", res.Err)
	}

	snap, _ := c.Inspect(context.Background(), h)
	if snap.Status != ledger.RunStatusCompleted {
		t.Fatalf("run status = %q, want %q", snap.Status, ledger.RunStatusCompleted)
	}
	if len(snap.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(snap.Tasks))
	}
	for _, task := range snap.Tasks {
		if task.Status != string(ledger.TaskStatusCompleted) {
			t.Fatalf("task %q status = %q, want %q", task.TaskID, task.Status, ledger.TaskStatusCompleted)
		}
	}
}

func TestIntegration_TaskFailedDependencyBlocked(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fail", staticHandler{err: errors.New("fail")})
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

	res, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	snap, _ := c.Inspect(context.Background(), h)
	if snap.Status != ledger.RunStatusFailed {
		t.Fatalf("run status = %q, want %q", snap.Status, ledger.RunStatusFailed)
	}

	var childFound bool
	for _, task := range snap.Tasks {
		if task.TaskID == "child" && task.Status == string(ledger.TaskStatusBlocked) {
			childFound = true
		}
		if task.TaskID == "parent" && task.Status != string(ledger.TaskStatusFailed) {
			t.Fatalf("parent status = %q, want %q", task.Status, ledger.TaskStatusFailed)
		}
	}
	if !childFound {
		t.Fatal("child task not found with blocked status")
	}
	_ = res
}

func TestIntegration_CancelSetsRunAndTaskToCanceled(t *testing.T) {
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
	<-started

	if err := c.Cancel(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != ledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want %q", snap.Status, ledger.RunStatusCanceled)
	}
	if len(snap.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap.Tasks))
	}
	if snap.Tasks[0].Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("task status = %q, want %q", snap.Tasks[0].Status, ledger.TaskStatusCanceled)
	}
	if snap.CompletedAt == nil {
		t.Fatal("expected completed time for canceled run")
	}
}

func TestIntegration_InspectDuringExecutionShowsRunning(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	canProceed := make(chan struct{})
	_ = d.Register(runtime.Subagent, "slow", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(started)
		select {
		case <-canProceed:
			return json.RawMessage(`{"done":true}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "slow"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for task handler to start (channel sync, no sleep).
	<-started

	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	// Run should be running (not created, not completed)
	if snap.Status != ledger.RunStatusRunning {
		t.Fatalf("in-progress run status = %q, want %q", snap.Status, ledger.RunStatusRunning)
	}
	if len(snap.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(snap.Tasks))
	}
	// Task should be running, not queued
	if snap.Tasks[0].Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("in-progress task status = %q, want %q", snap.Tasks[0].Status, ledger.TaskStatusRunning)
	}

	close(canProceed)
	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
}

func TestIntegration_ConcurrentMultipleRuns(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 2})
	c := New(repo, p)

	const runs = 10
	type result struct {
		idx int
		err error
	}
	results := make(chan result, runs)
	var wg sync.WaitGroup

	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h, err := c.Spawn(context.Background(), []subagents.Task{
				{Name: "worker"},
			}, "")
			if err != nil {
				results <- result{i, err}
				return
			}
			_, err = c.Join(context.Background(), h)
			results <- result{i, err}
		}(i)
	}
	wg.Wait()
	close(results)

	var failures int
	for r := range results {
		if r.err != nil {
			t.Errorf("run %d failed: %v", r.idx, r.err)
			failures++
		}
	}
	if failures > 0 {
		t.Fatalf("%d/%d runs failed", failures, runs)
	}
}

func TestIntegration_EventSequenceOrdered(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 1 {
		t.Fatal("expected at least 1 event")
	}
	for i := 1; i < len(events); i++ {
		if events[i].Sequence <= events[i-1].Sequence {
			t.Fatalf("event %d sequence %d not greater than previous %d",
				i, events[i].Sequence, events[i-1].Sequence)
		}
	}
}

func TestIntegration_RedactedOutputRefNotRaw(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"secret":"data"}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
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
	// Must be a bounded reference, not raw content
	if task.OutputRef == `{"secret":"data"}` {
		t.Fatal("raw output stored in ledger, expected bounded reference")
	}
	if task.OutputRef == "" {
		t.Fatal("expected non-empty output ref")
	}
}

func TestIntegration_SpawnIdempotency(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h1, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
	}, "key-1")
	if err != nil {
		t.Fatal(err)
	}

	h2, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
	}, "key-1")
	if err != nil {
		t.Fatal(err)
	}

	if h1 != h2 {
		t.Fatal("duplicate Spawn with same key returned different handle")
	}
	// Both handles should lead to same run completing
	_, err = c.Join(context.Background(), h1)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(context.Background(), h1)
	if snap.Status != ledger.RunStatusCompleted {
		t.Fatalf("run status = %q, want %q", snap.Status, ledger.RunStatusCompleted)
	}
}

func TestIntegration_DefensiveCopyIsolation(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "worker"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}

	snap1, _ := c.Inspect(context.Background(), h)
	snap2, _ := c.Inspect(context.Background(), h)

	// Mutate snap1
	snap1.Status = ledger.RunStatusCreated
	snap1.Tasks[0].Status = string(ledger.TaskStatusQueued)
	snap1.Labels["injected"] = "value"

	// snap2 must be unchanged
	if snap2.Status != ledger.RunStatusCompleted {
		t.Fatal("defensive copy failed: snap2 status was mutated via snap1")
	}
	if snap2.Tasks[0].Status != string(ledger.TaskStatusCompleted) {
		t.Fatal("defensive copy failed: snap2 task status was mutated via snap1")
	}
	if _, ok := snap2.Labels["injected"]; ok {
		t.Fatal("defensive copy failed: snap2 labels were mutated via snap1")
	}
}
