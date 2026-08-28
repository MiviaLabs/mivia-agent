package clichat

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// spawnNamespacedRun is spawnBroadcastRun's sibling (send_to_task_broadcast_test.go):
// it admits tasks with BOTH a real, namespaced id and the RawID
// subagents.Task.RawID field dispatch_tasks' buildTasks actually sets
// (internal/cliorchestrate/dispatch.go), the shape resolveSendTargetTaskID/
// cliorchestrate.ResolveTaskID resolve against. spawnBroadcastRun's tasks
// never set RawID (it constructs Task{ID: id} directly, bypassing
// dispatch_tasks), so it cannot exercise this resolution path.
func spawnNamespacedRun(t *testing.T, pairs map[string]string) *broadcastRun {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	cfg := config.DefaultSubagentConfig
	started := make(map[string]*signalOnce, len(pairs))
	for realID := range pairs {
		started[realID] = newSignalOnce()
	}
	// Every task blocks until t.Cleanup releases it, exactly like
	// spawnBroadcastRun's live-map tasks - a task that completes (and
	// gets its mailbox fenced terminal) before the test's send_to_task
	// call would fail delivery for a reason unrelated to id resolution.
	hold := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(hold) }) })
	if err := d.Register(runtime.Subagent, "worker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		if id, ok := runtime.TaskIdentityFrom(ctx); ok {
			if s, ok := started[id.TaskID]; ok {
				s.fire()
			}
		}
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	pool := subagents.New(d, subagents.Policy{Workers: 2})
	c := coordinator.New(repo, pool)
	cliorchestrate.CoordinatorsForTest.Store(d, c)
	cliorchestrate.CoordinatorReposForTest.Store(d, repo)
	t.Cleanup(func() {
		cliorchestrate.CoordinatorsForTest.Delete(d)
		cliorchestrate.CoordinatorReposForTest.Delete(d)
	})
	tasks := make([]subagents.Task, 0, len(pairs))
	for realID, rawID := range pairs {
		tasks = append(tasks, subagents.Task{ID: realID, RawID: rawID, Name: "worker", AgentName: "worker", Timeout: 10 * time.Second})
	}
	h, err := c.Spawn(context.Background(), tasks, "")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	runID := snap.RunID
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-namespaced"})
	cliorchestrate.StoreTestRunHandle(runID, c, h, repo, d, "sess-namespaced")
	t.Cleanup(func() {
		cliorchestrate.RunHandlesForTest.Delete(runID)
	})
	tool := &sendToTaskTool{dispatcher: d, cfg: cfg, repo: repo}
	return &broadcastRun{
		tool: tool, coord: c, handle: h, runID: runID, ctx: ctx,
		release: func() {}, started: started, fenced: map[string]*signalOnce{},
	}
}

// TestSendToTaskResolvesNamespacedTaskID pins the fix for a real,
// reachable bug: dispatch_tasks mints each task's real internal id as
// namespace+":"+rawID (internal/cliorchestrate/dispatch.go's
// dispatchNamespace/namespacedTaskID) but strips that prefix from every
// model-visible surface, so the model only ever learns its own raw id
// (e.g. "t1"). Before resolveSendTargetTaskID, send_to_task passed that
// raw id straight through to the coordinator, which lazily creates a
// fresh, empty mailbox for whatever key it's given - so the call reported
// delivered:true while the message was silently orphaned, never reaching
// the live task (real id "ns1:t1").
func TestSendToTaskResolvesNamespacedTaskID(t *testing.T) {
	run := spawnNamespacedRun(t, map[string]string{"ns1:t1": "t1"})
	run.waitStarted(t, "ns1:t1")

	out, err := run.tool.Execute(run.ctx, json.RawMessage(`{
		"run_id":"`+run.runID+`",
		"task_id":"t1",
		"kind":"steer",
		"body":"go faster"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Delivered bool `json:"delivered"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !resp.Delivered {
		t.Fatalf("expected delivered=true: the model's raw id %q must resolve to the real task %q, got %s", "t1", "ns1:t1", out)
	}
}

// TestSendToTaskBroadcastResolvesNamespacedTaskIDs is the broadcast-path
// sibling of TestSendToTaskResolvesNamespacedTaskID: task_ids must resolve
// the same way per-target, and the result map must stay keyed by the
// model's own raw ids (not the resolved real ones), so the model can
// still correlate each result with the id it requested.
func TestSendToTaskBroadcastResolvesNamespacedTaskIDs(t *testing.T) {
	run := spawnNamespacedRun(t, map[string]string{"ns2:a": "a", "ns2:b": "b"})
	run.waitStarted(t, "ns2:a", "ns2:b")

	out, err := run.tool.Execute(run.ctx, json.RawMessage(`{
		"run_id":"`+run.runID+`",
		"task_ids":["a","b"],
		"kind":"steer",
		"body":"go faster"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Results map[string]struct {
			Delivered bool   `json:"delivered"`
			Error     string `json:"error,omitempty"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	for _, raw := range []string{"a", "b"} {
		r, ok := resp.Results[raw]
		if !ok {
			t.Fatalf("expected a result keyed by the model's raw id %q, got %+v", raw, resp.Results)
		}
		if !r.Delivered {
			t.Errorf("task %q: expected delivered=true, got %+v", raw, r)
		}
	}
}
