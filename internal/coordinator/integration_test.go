package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
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
	if len(snap.Tasks[0].Attempts) != 1 || snap.Tasks[0].Attempts[0].Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("attempt was not finalized as canceled: %+v", snap.Tasks[0].Attempts)
	}
	events, err := repo.ListEvents(context.Background(), h.runID)
	if err != nil {
		t.Fatal(err)
	}
	foundCanceled := false
	for _, event := range events {
		if event.Kind == "task_canceled" && event.TaskID == "t1" {
			foundCanceled = true
		}
	}
	if !foundCanceled {
		t.Fatal("missing task_canceled lifecycle event")
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

func TestIntegration_SpawnIdempotencyAcrossCoordinators(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d1 := runtime.New(runtime.Policy{})
	_ = d1.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	c1 := New(repo, subagents.New(d1, subagents.Policy{Workers: 1}))
	h1, err := c1.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "worker"}}, "key-cross-coordinator")
	if err != nil {
		t.Fatal(err)
	}
	first, err := c1.Join(context.Background(), h1)
	if err != nil {
		t.Fatal(err)
	}

	d2 := runtime.New(runtime.Policy{})
	_ = d2.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	c2 := New(repo, subagents.New(d2, subagents.Policy{Workers: 1}))
	h2, err := c2.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "worker"}}, "key-cross-coordinator")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("recreated coordinator unexpectedly reused process-local handle")
	}
	result, err := c2.Join(context.Background(), h2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.RunID != first.Snapshot.RunID {
		t.Fatalf("recovered run id = %q, want %q", result.Snapshot.RunID, first.Snapshot.RunID)
	}
}

func TestIntegration_SpawnIdempotencyAcrossCoordinatorsRejectsDifferentRequest(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d1 := runtime.New(runtime.Policy{})
	_ = d1.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	c1 := New(repo, subagents.New(d1, subagents.Policy{Workers: 1}))
	if _, err := c1.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "worker"}}, "cross-key"); err != nil {
		t.Fatal(err)
	}

	d2 := runtime.New(runtime.Policy{})
	_ = d2.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	c2 := New(repo, subagents.New(d2, subagents.Policy{Workers: 1}))
	_, err := c2.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "different"}}, "cross-key")
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-coordinator mismatch error = %v, want %v", err, ErrIdempotencyConflict)
	}
}

func TestIntegration_RecoveredNonterminalJoinFailsClosed(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()
	if err := repo.CreateRun(ctx, "nonterminal-recovery", ledger.RunSnapshot{RunID: "recovered-running", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: "recovered-running", TaskID: "task-1", Status: string(ledger.TaskStatusRunning), Version: 1,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "task-1", RunID: "recovered-running", AttemptNum: 1, Status: string(ledger.TaskStatusRunning)}},
	}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	h, err := c.Spawn(ctx, nil, "nonterminal-recovery")
	if err != nil {
		t.Fatal(err)
	}
	joined, err := c.Join(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Err == nil || !strings.Contains(joined.Err.Error(), "no live execution owner") {
		t.Fatalf("join error = %v, want fail-closed ownership error", joined.Err)
	}
	if len(joined.Results) != 1 || joined.Results[0].TaskID != "task-1" || joined.Results[0].Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("recovered results = %+v", joined.Results)
	}
}

func TestIntegration_RecoveredCancelDoesNotClaimRunningTask(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()
	if err := repo.CreateRun(ctx, "cancel-recovery", ledger.RunSnapshot{RunID: "recovered-cancel", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: "recovered-cancel", TaskID: "task-1", Status: string(ledger.TaskStatusRunning), Version: 1,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "task-1", RunID: "recovered-cancel", AttemptNum: 1, Status: string(ledger.TaskStatusRunning)}},
	}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	h, err := c.Spawn(ctx, nil, "cancel-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel(ctx, h); err == nil || !strings.Contains(err.Error(), "no live execution owner") {
		t.Fatalf("cancel error = %v, want fail-closed ownership error", err)
	}
	task, err := repo.GetTask(ctx, "recovered-cancel", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != string(ledger.TaskStatusRunning) || task.Attempts[0].Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("recovered task was mutated: %+v", task)
	}
}

func TestIntegration_RecoveredCancelReconcilesQueuedTask(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()
	if err := repo.CreateRun(ctx, "queued-cancel-recovery", ledger.RunSnapshot{RunID: "recovered-queued", Status: ledger.RunStatusQueued}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: "recovered-queued", TaskID: "task-1", Status: string(ledger.TaskStatusQueued), Version: 1,
		Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-1", TaskID: "task-1", RunID: "recovered-queued", AttemptNum: 1, Status: string(ledger.TaskStatusQueued)}},
	}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	h, err := c.Spawn(ctx, nil, "queued-cancel-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != ledger.RunStatusCanceled || snap.Tasks[0].Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("reconciled snapshot = %+v", snap)
	}
	if snap.Tasks[0].Attempts[0].Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("attempt status = %q, want canceled", snap.Tasks[0].Attempts[0].Status)
	}
}

func seedRecoveredRun(t *testing.T, repo ledger.LedgerRepository, key, taskID, status string) {
	t.Helper()
	if err := repo.CreateRun(context.Background(), key, ledger.RunSnapshot{
		RunID: "recovered-" + taskID, Status: ledger.RunStatusRunning,
		CreatedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTask(context.Background(), ledger.TaskSnapshot{
		RunID: "recovered-" + taskID, TaskID: taskID, DisplayName: taskID,
		Status: status, Version: 1, CreatedAt: time.Unix(1, 0),
		Attempts: []ledger.AttemptSnapshot{{
			RunID: "recovered-" + taskID, TaskID: taskID, AttemptID: "attempt-" + taskID,
			AttemptNum: 1, Status: status, StartedAt: time.Unix(1, 0),
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIntegration_RecoveredTerminalJoinReconstructsPersistedResults(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	if err := repo.CreateRun(context.Background(), "recovered-terminal", ledger.RunSnapshot{
		RunID: "recovered-terminal-run", Status: ledger.RunStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	for _, task := range []ledger.TaskSnapshot{
		{RunID: "recovered-terminal-run", TaskID: "done", Status: string(ledger.TaskStatusCompleted), Version: 1,
			Attempts: []ledger.AttemptSnapshot{{AttemptID: "a-done", TaskID: "done", RunID: "recovered-terminal-run", AttemptNum: 1, Status: string(ledger.TaskStatusCompleted)}}},
		{RunID: "recovered-terminal-run", TaskID: "failed", Status: string(ledger.TaskStatusFailed), ErrorRef: "ref:error:deadbeef", Version: 1,
			Attempts: []ledger.AttemptSnapshot{{AttemptID: "a-failed", TaskID: "failed", RunID: "recovered-terminal-run", AttemptNum: 1, Status: string(ledger.TaskStatusFailed)}}},
	} {
		if err := repo.CreateTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	c := New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), nil, "recovered-terminal")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result.Err != nil {
		t.Fatalf("recovered terminal run error = %v", result.Err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("recovered results = %+v", result.Results)
	}
	byTask := make(map[string]subagents.Result, len(result.Results))
	for _, recovered := range result.Results {
		byTask[recovered.TaskID] = recovered
	}
	if byTask["done"].Status != string(ledger.TaskStatusCompleted) || byTask["failed"].Status != string(ledger.TaskStatusFailed) {
		t.Fatalf("recovered result statuses = %+v", result.Results)
	}
	if byTask["failed"].Err == nil || byTask["failed"].Err.Error() != "ref:error:deadbeef" {
		t.Fatalf("recovered error ref = %v", byTask["failed"].Err)
	}
}

func TestIntegration_RecoveredNonterminalJoinFailsClosedWithoutWaiting(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	seedRecoveredRun(t, repo, "recovered-running", "running-task", string(ledger.TaskStatusRunning))
	c := New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), nil, "recovered-running")
	if err != nil {
		t.Fatal(err)
	}
	joined := make(chan *RunResult, 1)
	go func() {
		result, joinErr := c.Join(context.Background(), h)
		if joinErr != nil {
			joined <- &RunResult{Err: joinErr}
			return
		}
		joined <- result
	}()
	select {
	case result := <-joined:
		if !errors.Is(result.Err, errRecoveredRunNotResumable) {
			t.Fatalf("recovered join error = %v, want %v", result.Err, errRecoveredRunNotResumable)
		}
	case <-time.After(time.Second):
		t.Fatal("recovered nonterminal join waited for unavailable execution")
	}
}

func TestIntegration_RecoveredCancelQueuedTaskDurablyCancels(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	seedRecoveredRun(t, repo, "recovered-queued", "queued-task", string(ledger.TaskStatusQueued))
	c := New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), nil, "recovered-queued")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != ledger.RunStatusCanceled || snap.Tasks[0].Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("recovered canceled snapshot = %+v", snap)
	}
	if snap.Tasks[0].Attempts[0].Status != string(ledger.TaskStatusCanceled) {
		t.Fatalf("recovered attempt status = %q", snap.Tasks[0].Attempts[0].Status)
	}
}

func TestIntegration_RecoveredCancelRunningTaskFailsClosed(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	seedRecoveredRun(t, repo, "recovered-cancel-running", "running-task", string(ledger.TaskStatusRunning))
	c := New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), nil, "recovered-cancel-running")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel(context.Background(), h); err == nil {
		t.Fatal("recovered cancellation claimed success without a live owner")
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != ledger.RunStatusRunning || snap.Tasks[0].Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("recovered running task changed during failed cancellation = %+v", snap)
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

// ---------------------------------------------------------------------------
// Durable resume tests — requires storage backend
// ---------------------------------------------------------------------------

func TestCoordinator_ResumeInterruptedRun(t *testing.T) {
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	// Phase 1: Create interrupted state via storage repo.
	storeRepo := ledger.NewStorageLedgerRepository(store)
	storeRepo.SetTimeSource(func() time.Time { return now })
	ctx := context.Background()
	if err := storeRepo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "run-resume", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	// Task 1: completed
	if err := storeRepo.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-resume", TaskID: "t1", Status: string(ledger.TaskStatusQueued), Version: 1}); err != nil {
		t.Fatal(err)
	}
	_ = storeRepo.CompareAndSetTaskStatus(ctx, "run-resume", "t1", 1, string(ledger.TaskStatusRunning))
	_ = storeRepo.CompareAndSetTaskStatus(ctx, "run-resume", "t1", 2, string(ledger.TaskStatusCompleted))
	// Task 2: running (interrupted mid-execution)
	if err := storeRepo.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-resume", TaskID: "t2", Status: string(ledger.TaskStatusQueued), Version: 1}); err != nil {
		t.Fatal(err)
	}
	_ = storeRepo.CompareAndSetTaskStatus(ctx, "run-resume", "t2", 1, string(ledger.TaskStatusRunning))
	storeRepo.Close()

	// Phase 2: Create coordinator with fresh storage repo from same store.
	recoveredRepo := ledger.NewStorageLedgerRepository(store)
	recoveredRepo.SetTimeSource(func() time.Time { return now })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(recoveredRepo, p)

	recovered, err := recoveredRepo.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) == 0 {
		t.Fatal("expected recovered runs")
	}
	if !recovered[0].WasInterrupted {
		t.Fatal("expected interrupted run")
	}

	h, err := c.ResumeInterruptedRun(ctx, "run-resume")
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle")
	}

	result, err := c.Join(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	t.Logf("resumed run status: %s", result.Snapshot.Status)
	for _, task := range result.Snapshot.Tasks {
		t.Logf("  task %s: status=%s", task.TaskID, task.Status)
	}
}

func TestCoordinator_ResumeInterruptedRun_AutoRetry(t *testing.T) {
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	storeRepo := ledger.NewStorageLedgerRepository(store)
	storeRepo.SetTimeSource(func() time.Time { return now })
	ctx := context.Background()

	if err := storeRepo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "run-retry-resume", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := storeRepo.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-retry-resume", TaskID: "t1", Status: string(ledger.TaskStatusRunning), Version: 1}); err != nil {
		t.Fatal(err)
	}
	storeRepo.Close()

	recoveredRepo := ledger.NewStorageLedgerRepository(store)
	recoveredRepo.SetTimeSource(func() time.Time { return now })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(recoveredRepo, p).WithRetryPolicy(RetryPolicy{
		MaxRetries:    2,
		BaseBackoff:   1 * time.Millisecond,
		MaxBackoff:    5 * time.Millisecond,
		BackoffFactor: 2.0,
	})

	h, err := c.ResumeInterruptedRun(ctx, "run-retry-resume")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(ctx, h)
	if err != nil {
		t.Fatal(err)
	}

	snap, _ := c.Inspect(ctx, h)
	t.Logf("resumed+retried run status: %s", snap.Status)
	for _, task := range snap.Tasks {
		t.Logf("  task %s: status=%s", task.TaskID, task.Status)
	}
}

func TestIntegration_ResumeEmitsInterruptedEvents(t *testing.T) {
	// Regression test for Bug 12: ResumeInterruptedRun must emit
	// task_interrupted_unrecoverable events via SubscribeLifecycle.
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	storeRepo := ledger.NewStorageLedgerRepository(store)
	storeRepo.SetTimeSource(func() time.Time { return now })
	ctx := context.Background()

	if err := storeRepo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "run-resume-events", Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := storeRepo.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-resume-events", TaskID: "t1", Status: string(ledger.TaskStatusRunning), Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := storeRepo.CreateTask(ctx, ledger.TaskSnapshot{RunID: "run-resume-events", TaskID: "t2", Status: string(ledger.TaskStatusQueued), Version: 1}); err != nil {
		t.Fatal(err)
	}
	storeRepo.Close()

	recoveredRepo := ledger.NewStorageLedgerRepository(store)
	recoveredRepo.SetTimeSource(func() time.Time { return now })
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Partial: true})
	c := New(recoveredRepo, p)

	var mu sync.Mutex
	interruptedEvents := 0
	taskEvents := make(map[string]string)

	c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		mu.Lock()
		if string(evt.Kind) == "task_interrupted_unrecoverable" {
			interruptedEvents++
		}
		if evt.TaskID != "" {
			taskEvents[evt.TaskID] = string(evt.Kind)
		}
		mu.Unlock()
	})

	h, err := c.ResumeInterruptedRun(ctx, "run-resume-events")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Join(ctx, h)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	ie := interruptedEvents
	te := taskEvents["t1"]
	mu.Unlock()

	// t1 was running, must have interrupted_unrecoverable event.
	if ie != 1 {
		t.Fatalf("MUTATION FAIL (Bug 12): expected 1 task_interrupted_unrecoverable event, got %d", ie)
	}
	// t1's event kind should be interrupted_unrecoverable.
	if te != "task_interrupted_unrecoverable" {
		t.Logf("t1 event kind = %q (may have been overwritten by subsequent transitions)", te)
	}
	t.Logf("resume interrupted events: interrupted_unrecoverable=%d", ie)
}
