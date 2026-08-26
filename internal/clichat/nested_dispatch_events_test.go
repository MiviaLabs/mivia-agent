package clichat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// This file probes the UI-divergence half of a live production symptom: an
// outer dispatch_tasks whose subagent itself performed a nested dispatch, and
// the activity panel rows for the nested calls never resolved. The probe runs
// a real MultiStepHandler agent loop whose scripted model makes a nested
// dispatch tool call, captures every agent.Event through the handler's
// OnEvent sink, and asserts the nested call's ToolEnd event actually arrives
// with the matching ToolCallID after the work completes. A run that finishes
// with the ToolStart delivered but the ToolEnd missing IS the panel bug.
//
// Note on production wiring: the compiled mandatory denylist strips
// dispatch_tasks itself from every spawned agent's registry
// (tools.CompiledMandatoryDenylist), so a nested dispatch in production must
// travel through some other admitted tool surface. The probe mirrors that: a
// non-privileged tool whose Execute drives the REAL dispatchTasksTool on the
// same dispatcher, coordinator singleton, and shared ledger repo.

// nestedDispatchScriptCompleter scripts one multi_step loop: first turn calls
// the nested dispatch wrapper tool, second turn finishes.
type nestedDispatchScriptCompleter struct {
	turns atomic.Int32
}

func (c *nestedDispatchScriptCompleter) Name() string { return "nested-dispatch-events-test" }

func (c *nestedDispatchScriptCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "unused chat path", nil
}

func (c *nestedDispatchScriptCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

func (c *nestedDispatchScriptCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.turns.Add(1) == 1 {
		var call provider.ToolCall
		call.ID = "nested-dispatch-call-1"
		call.Type = "function"
		call.Function.Name = "nested_dispatch"
		call.Function.Arguments = `{}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	return &provider.Response{Content: "nested dispatch complete", FinishReason: "stop"}, nil
}

// nestedDispatchWrapperTool is a non-privileged tool that survives
// ScopeSpawned filtering and forwards to the real dispatch_tasks tool - the
// only way a spawned multi_step loop can reach a nested dispatch, given the
// compiled denylist.
type nestedDispatchWrapperTool struct {
	inner *cliorchestrate.DispatchTasksToolForTest
}

func (t *nestedDispatchWrapperTool) Name() string        { return "nested_dispatch" }
func (t *nestedDispatchWrapperTool) Description() string { return "dispatch nested leaf tasks" }
func (t *nestedDispatchWrapperTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func (t *nestedDispatchWrapperTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.inner.Execute(ctx, json.RawMessage(
		`{"tasks":[{"id":"nested-leaf-1","agent":"leaf","prompt":"leaf work 1"},{"id":"nested-leaf-2","agent":"leaf","prompt":"leaf work 2"}]}`))
}

// eventRecorder captures every agent.Event a handler emits, race-safely.
type eventRecorder struct {
	mu     sync.Mutex
	events []agent.Event
}

func (r *eventRecorder) record(e agent.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *eventRecorder) snapshot() []agent.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agent.Event, len(r.events))
	copy(out, r.events)
	return out
}

func dumpGoroutinesForNestedProbe(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	t.Logf("goroutine dump at probe failure:\n%s", buf.String())
}

// TestNestedDispatchToolEndEventDelivered drives outer dispatch_tasks ->
// pool -> MultiStepHandler agent loop -> nested dispatch tool ->
// RunThroughCoordinator -> leaf handler, and asserts the event stream the UI
// panel consumes is complete: the nested call's ToolStart has a matching
// ToolEnd with the same ToolCallID, and the subagent announces Done. A
// successful completion whose end-event never arrives reproduces the "panel
// row stuck running forever" divergence.
// nestedDispatchEventProbeExecResult is runNestedDispatchEventProbe's outcome.
type nestedDispatchEventProbeExecResult struct {
	body string
	err  error
}

// runNestedDispatchEventProbe wires outer dispatch_tasks -> pool ->
// MultiStepHandler agent loop -> nested dispatch tool ->
// RunThroughCoordinator -> leaf handler, runs it to completion (or fails the
// test on timeout/transport error), and returns the recorded event stream
// plus the shared ledger repo for the caller's assertions. Split out of the
// test body to stay under the function-LOC gate.
func runNestedDispatchEventProbe(t *testing.T) ([]agent.Event, ledger.LedgerRepository) {
	t.Helper()
	cfg := config.DefaultSubagentConfig
	d := runtime.New(runtime.Policy{MaxDepth: 6})
	t.Cleanup(d.Close)
	repo := ledger.NewMemoryLedgerRepository()

	if err := d.Register(runtime.Subagent, "leaf", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`"leaf-done"`), nil
	})); err != nil {
		t.Fatal(err)
	}

	nestedTool := cliorchestrate.NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "leaf"))
	reg := tools.NewRegistry()
	reg.Register(&nestedDispatchWrapperTool{inner: nestedTool})

	rec := &eventRecorder{}
	handler := &subagents.MultiStepHandler{
		Completer:    &nestedDispatchScriptCompleter{},
		FullRegistry: reg,
		Dispatcher:   d,
		Model:        "test-model",
		SystemPrompt: "You are a mid-level agent that dispatches nested work.",
		MaxSteps:     8,
		ToolTimeout:  30 * time.Second,
		MaxTokens:    256,
		OnEvent:      rec.record,
	}
	if err := d.Register(runtime.Subagent, "midagent", handler); err != nil {
		t.Fatal(err)
	}
	outer := cliorchestrate.NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "midagent"))

	done := make(chan nestedDispatchEventProbeExecResult, 1)
	ctx, cancel := context.WithTimeout(
		runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "nested-events-session"}),
		30*time.Second)
	defer cancel()
	go func() {
		body, err := outer.Execute(ctx, json.RawMessage(
			`{"tasks":[{"id":"mid-1","agent":"midagent","prompt":"do nested work"}]}`))
		done <- nestedDispatchEventProbeExecResult{body: body, err: err}
	}()

	var res nestedDispatchEventProbeExecResult
	select {
	case res = <-done:
	case <-time.After(40 * time.Second):
		dumpGoroutinesForNestedProbe(t)
		t.Fatal("PROBE FAILURE: outer dispatch with a nested dispatch inside a MultiStepHandler loop never returned within 40s")
	}
	if res.err != nil {
		t.Fatalf("outer dispatch transport error: %v (body=%s)", res.err, res.body)
	}
	if !strings.Contains(res.body, `"completed"`) {
		t.Fatalf("outer mid task did not complete: %s", res.body)
	}

	// The pool worker returns only after the handler (and its deferred
	// SubagentDone emit) finished, so the recorder is complete here.
	return rec.snapshot(), repo
}

func TestNestedDispatchToolEndEventDelivered(t *testing.T) {
	events, repo := runNestedDispatchEventProbe(t)

	var startID string
	var sawEnd, sawDone bool
	var unmatchedStarts []string
	starts := map[string]bool{}
	for _, e := range events {
		switch e.Kind {
		case agent.EventToolStart:
			starts[e.ToolCallID] = false
			if e.Name == "nested_dispatch" {
				startID = e.ToolCallID
			}
		case agent.EventToolEnd:
			starts[e.ToolCallID] = true
			if e.Name == "nested_dispatch" && e.ToolCallID == startID && startID != "" {
				sawEnd = true
			}
		case agent.EventSubagentDone:
			sawDone = true
		}
	}
	for id, ended := range starts {
		if !ended {
			unmatchedStarts = append(unmatchedStarts, id)
		}
	}

	if startID == "" {
		t.Fatalf("nested_dispatch ToolStart event never arrived - the nested call is invisible to the UI panel; events: %s", describeEventsForNestedProbe(events))
	}
	if !sawEnd {
		t.Fatalf("PROBE FAILURE: nested_dispatch completed but its ToolEnd event (ToolCallID=%q) never arrived - this is the UI panel row that never resolves; events: %s",
			startID, describeEventsForNestedProbe(events))
	}
	if len(unmatchedStarts) > 0 {
		t.Fatalf("PROBE FAILURE: tool calls with a ToolStart but no ToolEnd: %v; events: %s",
			unmatchedStarts, describeEventsForNestedProbe(events))
	}
	if !sawDone {
		t.Fatalf("PROBE FAILURE: subagent finished but never emitted SubagentDone - the panel cannot retire the agent; events: %s", describeEventsForNestedProbe(events))
	}

	// The shared ledger must have settled every run and task the nesting
	// created (the outer run plus the nested run), mirroring the terminal
	// walk the cliorchestrate probes do.
	requireNestedProbeLedgerTerminal(t, repo)
}

func describeEventsForNestedProbe(events []agent.Event) string {
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "{kind=%v name=%s id=%s} ", e.Kind, e.Name, e.ToolCallID)
	}
	return b.String()
}

func requireNestedProbeLedgerTerminal(t *testing.T, repo ledger.LedgerRepository) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var leaked string
	for time.Now().Before(deadline) {
		leaked = ""
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		runs, err := repo.ListRuns(ctx)
		if err != nil {
			cancel()
			t.Fatalf("ListRuns: %v", err)
		}
		for _, r := range runs {
			switch r.Status {
			case ledger.RunStatusCompleted, ledger.RunStatusFailed, ledger.RunStatusCanceled:
			default:
				leaked += fmt.Sprintf("run %s status=%s; ", r.RunID, r.Status)
			}
			tasksInRun, err := repo.ListTasks(ctx, r.RunID)
			if err != nil {
				cancel()
				t.Fatalf("ListTasks(%s): %v", r.RunID, err)
			}
			for _, task := range tasksInRun {
				if !coordinator.IsTaskTerminal(string(task.Status)) {
					leaked += fmt.Sprintf("run %s task %s status=%s; ", r.RunID, task.TaskID, task.Status)
				}
			}
		}
		cancel()
		if leaked == "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	dumpGoroutinesForNestedProbe(t)
	t.Fatalf("PROBE FAILURE: ledger rows still non-terminal 10s after the outer dispatch returned: %s", leaked)
}
