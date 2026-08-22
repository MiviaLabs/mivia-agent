package clichat

import (
	"context"
	"encoding/json"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// broadcastRun bundles the pieces of a spawned broadcast run plus the
// deterministic synchronization signals the tests use instead of time.Sleep:
//   - started is fired by a task's handler when it begins, so a test can wait
//     until a broadcast provably targets live (running) handlers;
//   - fenced is fired by the pool's OnTaskDone wrapper right after the
//     coordinator's own onTaskDone has run MarkTaskMailboxTerminal, so a test
//     can wait until a terminal task's mailbox is fenced before broadcasting
//     (the exact condition the removed waitMailboxTerminal sleep-polled for).
type broadcastRun struct {
	tool    *sendToTaskTool
	coord   coordinator.Coordinator
	handle  *coordinator.RunHandle
	runID   string
	ctx     context.Context
	release func()
	started map[string]*signalOnce
	fenced  map[string]*signalOnce
}

// signalOnce is a one-shot channel close guarded by sync.Once, so a signal can
// never panic on a double close (e.g. if a task were ever dispatched twice).
type signalOnce struct {
	once sync.Once
	ch   chan struct{}
}

func newSignalOnce() *signalOnce { return &signalOnce{ch: make(chan struct{})} }

func (s *signalOnce) fire() { s.once.Do(func() { close(s.ch) }) }

// spawnBroadcastRun spawns a run whose tasks all share the "worker" handler.
// Tasks named in `live` block on a release channel (kept live); all others
// return immediately (terminal). The run handle is registered in cliorchestrate.RunHandlesForTest
// under an owner caller so sendToTaskTool's principal gate passes.
func spawnBroadcastRun(t *testing.T, live map[string]bool, taskIDs ...string) *broadcastRun {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	cfg := config.DefaultSubagentConfig
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	started := make(map[string]*signalOnce, len(taskIDs))
	fenced := make(map[string]*signalOnce, len(taskIDs))
	for _, id := range taskIDs {
		started[id] = newSignalOnce()
		fenced[id] = newSignalOnce()
	}
	_ = d.Register(runtime.Subagent, "worker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		if id, ok := runtime.TaskIdentityFrom(ctx); ok {
			if s, ok := started[id.TaskID]; ok {
				s.fire() // deterministic "handler began" signal
			}
			if live[id.TaskID] {
				select {
				case <-hold:
				case <-ctx.Done():
				}
			}
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))
	pool := subagents.New(d, subagents.Policy{Workers: 2})
	c := coordinator.New(repo, pool)
	// Observe the mailbox fence deterministically: coordinator.New installs its
	// own OnTaskDone (c.onTaskDone), whose last step for a completed task is
	// MarkTaskMailboxTerminal. Wrap it so fenced fires only after that fence has
	// run — the exact condition waitMailboxTerminal used to sleep-poll for.
	origOnTaskDone := pool.OnTaskDone
	pool.OnTaskDone = func(ctx context.Context, t subagents.Task, r subagents.Result) {
		if origOnTaskDone != nil {
			origOnTaskDone(ctx, t, r)
		}
		if s, ok := fenced[t.ID]; ok {
			s.fire()
		}
	}
	cliorchestrate.CoordinatorsForTest.Store(d, c)
	cliorchestrate.CoordinatorReposForTest.Store(d, repo)
	t.Cleanup(func() {
		cliorchestrate.CoordinatorsForTest.Delete(d)
		cliorchestrate.CoordinatorReposForTest.Delete(d)
	})
	tasks := make([]subagents.Task, 0, len(taskIDs))
	for _, id := range taskIDs {
		tasks = append(tasks, subagents.Task{ID: id, Name: "worker", AgentName: "worker", Timeout: 10 * time.Second})
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
	caller := runtime.Caller{SessionID: "sess-broadcast"}
	ctx := runtime.ContextWithCaller(context.Background(), caller)
	cliorchestrate.StoreTestRunHandle(runID, c, h, repo, d, "sess-broadcast")
	t.Cleanup(func() {
		cliorchestrate.RunHandlesForTest.Delete(runID)
		release()
	})
	tool := &sendToTaskTool{dispatcher: d, cfg: cfg, repo: repo}
	return &broadcastRun{
		tool: tool, coord: c, handle: h, runID: runID, ctx: ctx,
		release: release, started: started, fenced: fenced,
	}
}

// joinReleased completes a live-blocked run and joins it so the test leaves no
// dangling pool goroutines.
func joinReleased(t *testing.T, run *broadcastRun) {
	t.Helper()
	run.release()
	joinCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := run.coord.Join(joinCtx, run.handle); err != nil {
		t.Fatalf("join after release: %v", err)
	}
}

// waitStarted blocks until each named task's handler has begun, so a broadcast
// sent afterwards deterministically targets live (running) handlers.
func (r *broadcastRun) waitStarted(t *testing.T, taskIDs ...string) {
	t.Helper()
	for _, id := range taskIDs {
		s, ok := r.started[id]
		if !ok {
			t.Fatalf("no start signal registered for task %q", id)
		}
		select {
		case <-s.ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("handler for task %q never started within 10s", id)
		}
	}
}

// waitFenced blocks until each named task's mailbox has been fenced terminal
// by the coordinator. This is the condition the old waitMailboxTerminal
// sleep-polled for; here it is a plain channel wait (no time.Sleep), so a
// broadcast sent afterwards deterministically reports delivered=false.
func (r *broadcastRun) waitFenced(t *testing.T, taskIDs ...string) {
	t.Helper()
	for _, id := range taskIDs {
		s, ok := r.fenced[id]
		if !ok {
			t.Fatalf("no fence signal registered for task %q", id)
		}
		select {
		case <-s.ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("mailbox for task %q never fenced terminal within 10s", id)
		}
	}
}

// TestSendToTaskBroadcastDeliversToAllLive: a steer broadcast to two live
// tasks delivers to both and persists a durable message per target.
func TestSendToTaskBroadcastDeliversToAllLive(t *testing.T) {
	run := spawnBroadcastRun(t,
		map[string]bool{"t-live-1": true, "t-live-2": true}, "t-live-1", "t-live-2")
	defer joinReleased(t, run)
	// Deterministic: wait until both live handlers are running so the broadcast
	// provably reaches them while live (no time.Sleep).
	run.waitStarted(t, "t-live-1", "t-live-2")

	out, err := run.tool.Execute(run.ctx, json.RawMessage(`{
		"run_id":"`+run.runID+`",
		"task_ids":["t-live-1","t-live-2"],
		"kind":"steer",
		"body":"stop expanding scope"
	}`))
	if err != nil {
		t.Fatalf("broadcast execute: %v", err)
	}
	var res struct {
		Status  string                    `json:"status"`
		Results map[string]map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if res.Status != "sent" {
		t.Fatalf("status = %q, want sent", res.Status)
	}
	for _, id := range []string{"t-live-1", "t-live-2"} {
		entry, ok := res.Results[id]
		if !ok {
			t.Fatalf("missing result for %s in %s", id, out)
		}
		if entry["delivered"] != true {
			t.Fatalf("%s delivered = %v, want true (results=%s)", id, entry["delivered"], out)
		}
		if errStr, _ := entry["error"].(string); errStr != "" {
			t.Fatalf("%s unexpected error %q", id, errStr)
		}
	}
	// Each target has a durable steer in the ledger.
	for _, id := range []string{"t-live-1", "t-live-2"} {
		list, err := run.coord.ListRunMessages(context.Background(), run.runID, id)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range list {
			if m.Kind == agentmsg.KindSteer && strings.Contains(m.Synopsis, "stop expanding scope") {
				found = true
			}
		}
		if !found {
			t.Fatalf("no durable steer for %s: %+v", id, list)
		}
	}
}

// TestSendToTaskBroadcastMixedLiveTerminal: a broadcast to one live and one
// terminal task succeeds as a call and reports per-task results — delivered
// true for the live child, delivered false for the terminal one.
func TestSendToTaskBroadcastMixedLiveTerminal(t *testing.T) {
	run := spawnBroadcastRun(t,
		map[string]bool{"t-live": true}, "t-term", "t-live")
	defer joinReleased(t, run)
	// Deterministic (no time.Sleep): wait until t-term's handler has completed
	// and its mailbox is fenced terminal, and until t-live's handler is running,
	// so the broadcast provably sees one terminal and one live task.
	run.waitFenced(t, "t-term")
	run.waitStarted(t, "t-live")

	out, err := run.tool.Execute(run.ctx, json.RawMessage(`{
		"run_id":"`+run.runID+`",
		"task_ids":["t-term","t-live"],
		"kind":"steer",
		"body":"nudge"
	}`))
	if err != nil {
		t.Fatalf("broadcast execute: %v", err)
	}
	var res struct {
		Status  string                    `json:"status"`
		Results map[string]map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if res.Status != "sent" {
		t.Fatalf("status = %q, want sent (call must succeed despite one terminal target)", res.Status)
	}
	if entry := res.Results["t-term"]; entry == nil || entry["delivered"] != false {
		t.Fatalf("t-term result = %v, want delivered:false (results=%s)", entry, out)
	}
	if entry := res.Results["t-live"]; entry == nil || entry["delivered"] != true {
		t.Fatalf("t-live result = %v, want delivered:true (results=%s)", entry, out)
	}
}

// TestSendToTaskBroadcastValidation: exactly one of task_id / task_ids must be
// provided, and task_ids must be non-empty.
func TestSendToTaskBroadcastValidation(t *testing.T) {
	tool := &sendToTaskTool{
		dispatcher: runtime.New(runtime.Policy{}),
		cfg:        config.DefaultSubagentConfig,
		repo:       ledger.NewMemoryLedgerRepository(),
	}
	cases := []struct {
		name string
		args string
		want string
	}{
		{"neither", `{"run_id":"r","kind":"steer","body":"x"}`, "exactly one of task_id or task_ids is required"},
		{"both", `{"run_id":"r","task_id":"a","task_ids":["b"],"kind":"steer","body":"x"}`, "mutually exclusive"},
		{"empty task_ids", `{"run_id":"r","task_ids":[],"kind":"steer","body":"x"}`, "task_ids must contain at least one task_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), json.RawMessage(tc.args))
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestSendToTaskSingleTaskPath: the task_id path still returns the original
// single-target envelope (status/message_id/delivered, no results map).
func TestSendToTaskSingleTaskPath(t *testing.T) {
	run := spawnBroadcastRun(t,
		map[string]bool{"t-single": true}, "t-single")
	defer joinReleased(t, run)
	run.waitStarted(t, "t-single")

	out, err := run.tool.Execute(run.ctx, json.RawMessage(`{
		"run_id":"`+run.runID+`",
		"task_id":"t-single",
		"kind":"steer",
		"body":"single path"
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if res["status"] != "sent" || res["delivered"] != true {
		t.Fatalf("out = %s", out)
	}
	if mid, _ := res["message_id"].(string); mid == "" {
		t.Fatalf("single path must include message_id: %s", out)
	}
	if _, hasResults := res["results"]; hasResults {
		t.Fatalf("single path must not include a results map: %s", out)
	}
}
