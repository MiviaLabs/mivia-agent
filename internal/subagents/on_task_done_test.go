package subagents

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// TestPoolOnTaskDoneReceivesStampedContextAndResult pins plan R9's pool seam: a
// Pool with OnTaskDone set receives a callback for each task, carrying the
// STAMPED per-task context (runtime.TaskIdentityFrom resolves the RunID and
// TaskID the pool's ContextForTask injected) and the computed result status.
func TestPoolOnTaskDoneReceivesStampedContextAndResult(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", handlerFunc(func(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"done":true}`), nil
	}))
	_ = d.Register(runtime.Subagent, "fail", handlerFunc(func(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
		return nil, context.Canceled
	}))

	var mu sync.Mutex
	type call struct{ runID, taskID, status string }
	var calls []call
	p := New(d, Policy{Workers: 2})
	p.ContextForTask = func(ctx context.Context, taskID string) context.Context {
		// Standalone pool has no coordinator, so stamp identity explicitly,
		// exactly like coordinator.contextForTask does.
		return runtime.ContextWithTaskIdentity(ctx, runtime.TaskIdentity{
			RunID: "run-1", TaskID: taskID, Agent: "agent-" + taskID,
		})
	}
	p.OnTaskDone = func(ctx context.Context, task Task, r Result) {
		id, ok := runtime.TaskIdentityFrom(ctx)
		if !ok {
			t.Errorf("OnTaskDone context carries no TaskIdentity (run=%q task=%q)", id.RunID, id.TaskID)
			return
		}
		mu.Lock()
		calls = append(calls, call{runID: id.RunID, taskID: id.TaskID, status: r.Status})
		mu.Unlock()
	}

	results, err := p.Run(context.Background(), []Task{
		{ID: "t1", Name: "a"},
		{ID: "t2", Name: "a"},
		{ID: "t3", Name: "fail"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("OnTaskDone calls = %d, want 3", len(calls))
	}
	statusByTask := map[string]string{}
	for _, c := range calls {
		if c.runID != "run-1" {
			t.Fatalf("callback runID = %q, want run-1", c.runID)
		}
		statusByTask[c.taskID] = c.status
	}
	if statusByTask["t1"] != "completed" || statusByTask["t2"] != "completed" {
		t.Fatalf("completed task statuses = %v, want completed", statusByTask)
	}
	if statusByTask["t3"] != "canceled" {
		t.Fatalf("failed task status = %q, want canceled (ctx.Canceled)", statusByTask["t3"])
	}
}

// TestPoolNilOnTaskDoneIsNoOp: the default (nil) OnTaskDone must be a no-op —
// existing standalone pool runs keep working without any callback.
func TestPoolNilOnTaskDoneIsNoOp(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", handlerFunc(func(_ context.Context, _ runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"done":true}`), nil
	}))
	p := New(d, Policy{Workers: 1}) // OnTaskDone left nil (default)
	got, err := p.Run(context.Background(), []Task{{ID: "t1", Name: "a"}})
	if err != nil || len(got) != 1 || got[0].Status != "completed" {
		t.Fatalf("nil OnTaskDone must be a no-op: got=%+v err=%v", got, err)
	}
}
