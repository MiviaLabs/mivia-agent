// subagent_task_routes_test.go proves the two independent guards on the
// UI route-publish seam (subagent_task_routes.go) one at a time - no sink
// installed at all, and a sink explicitly removed with nil - plus a
// positive control that a real dispatch through the real dispatch_tasks
// tool publishes one route per task, keyed the way the UI keys its rows.
package cliorchestrate

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// recordedRoute is one published (callID, runID, taskID) triple plus the
// coordinator it was published with.
type recordedRoute struct {
	coord  coordinator.Coordinator
	callID string
	runID  string
	taskID string
}

// routeRecorder is a test sink that records every published route.
type routeRecorder struct {
	mu     sync.Mutex
	routes []recordedRoute
}

func (r *routeRecorder) sink(c coordinator.Coordinator, callID, runID, taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, recordedRoute{coord: c, callID: callID, runID: runID, taskID: taskID})
}

func (r *routeRecorder) snapshot() []recordedRoute {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedRoute, len(r.routes))
	copy(out, r.routes)
	return out
}

// installRouteSink installs fn for one test and restores the previous sink
// afterwards. The sink is package-level process state, so no test that
// touches it may run in parallel with another that does.
func installRouteSink(t *testing.T, fn func(coordinator.Coordinator, string, string, string)) {
	t.Helper()
	prev := subagentTaskRouteSink.Load()
	SetSubagentTaskRouteSink(fn)
	t.Cleanup(func() { subagentTaskRouteSink.Store(prev) })
}

// TestRegisterSubagentTaskRoutes_NoSinkInstalledIsNoop isolates
// registerSubagentTaskRoutes' `p == nil` guard: with no sink installed at
// all - every headless one-shot run, and every process that never starts
// the TUI - publishing must be a clean no-op. A mutant that flipped the
// guard to `p != nil` would dereference a nil pointer and panic here.
func TestRegisterSubagentTaskRoutes_NoSinkInstalledIsNoop(t *testing.T) {
	installRouteSink(t, nil)
	// Undo installRouteSink's own store so the slot is genuinely empty,
	// which is the state this guard exists for.
	subagentTaskRouteSink.Store(nil)

	registerSubagentTaskRoutes(nil, "run-1", []subagents.Task{{ID: "call:t1"}})
}

// TestSetSubagentTaskRouteSink_NilRemovesTheSink isolates
// SetSubagentTaskRouteSink's `fn == nil` guard. Removing the sink must
// store a nil POINTER, not a pointer to a nil func: a mutant that flipped
// the guard to `fn != nil` would store the latter, and the publish path
// would then call a nil func and panic here.
func TestSetSubagentTaskRouteSink_NilRemovesTheSink(t *testing.T) {
	rec := &routeRecorder{}
	installRouteSink(t, rec.sink)
	SetSubagentTaskRouteSink(nil)

	registerSubagentTaskRoutes(nil, "run-1", []subagents.Task{{ID: "call:t1"}})

	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("removed sink still received %d route(s): %+v", len(got), got)
	}
}

// TestSetSubagentTaskRouteSink_InstalledSinkReceivesEveryTask is the
// positive control the two guard tests are measured against: a real
// (non-nil) sink receives one call per task, with callID and taskID both
// set to the task's own namespaced id.
func TestSetSubagentTaskRouteSink_InstalledSinkReceivesEveryTask(t *testing.T) {
	rec := &routeRecorder{}
	installRouteSink(t, rec.sink)

	registerSubagentTaskRoutes(nil, "run-1", []subagents.Task{{ID: "call:t1"}, {ID: "call:t2"}})

	got := rec.snapshot()
	if len(got) != 2 {
		t.Fatalf("sink received %d route(s), want 2: %+v", len(got), got)
	}
	for _, r := range got {
		if r.runID != "run-1" {
			t.Fatalf("route %+v: runID = %q, want %q", r, r.runID, "run-1")
		}
		if r.callID != r.taskID {
			t.Fatalf("route %+v: callID and taskID must be the same namespaced id", r)
		}
	}
}

// TestDispatchTasks_PublishesRouteForEveryTask is the end-to-end half at
// this seam: a REAL dispatch_tasks Execute, through the real coordinator
// and pool, must publish one route per dispatched task keyed by
// "<dispatch tool call id>:<raw model task id>". That key is the contract
// with the UI - internal/ui/screen/conversation's dispatchTaskIDsAndNames
// builds a live row id exactly that way, and
// internal/uiadapter/subagent_reconstruct.go rebuilds a resumed session's
// row id the same way - so a drift in either direction breaks the cancel
// keys without breaking anything else.
func TestDispatchTasks_PublishesRouteForEveryTask(t *testing.T) {
	rec := &routeRecorder{}
	installRouteSink(t, rec.sink)

	d := runtime.New(runtime.Policy{MaxDepth: 4})
	t.Cleanup(d.Close)
	repo := ledger.NewMemoryLedgerRepository()
	if err := d.Register(runtime.Subagent, "leaf", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`"leaf-done"`), nil
	})); err != nil {
		t.Fatal(err)
	}
	tool := NewDispatchTasksToolConfigured(d, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "leaf"))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctx = runtime.ContextWithCaller(ctx, runtime.Caller{SessionID: "route-publish-session"})
	ctx = toolcallctx.WithToolCall(ctx, provider.ToolCall{ID: "call_routes_1", Name: ToolDispatchTasks})

	if _, err := tool.Execute(ctx, json.RawMessage(
		`{"tasks":[{"id":"alpha","agent":"leaf","prompt":"a"},{"id":"beta","agent":"leaf","prompt":"b"}]}`)); err != nil {
		t.Fatalf("dispatch_tasks Execute: %v", err)
	}

	got := rec.snapshot()
	if len(got) != 2 {
		t.Fatalf("published %d route(s), want one per dispatched task: %+v", len(got), got)
	}
	keys := make([]string, 0, len(got))
	for _, r := range got {
		if r.coord == nil {
			t.Fatalf("route %+v published a nil coordinator; the cancel path cannot resolve it", r)
		}
		if r.callID != r.taskID {
			t.Fatalf("route %+v: callID and taskID must be the same namespaced id", r)
		}
		if r.runID == "" {
			t.Fatalf("route %+v: runID is empty", r)
		}
		keys = append(keys, r.callID)
	}
	sort.Strings(keys)
	want := []string{"call_routes_1:alpha", "call_routes_1:beta"}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("published route keys = %v, want %v (the id the UI keys a subagent row on)", keys, want)
		}
	}
}
