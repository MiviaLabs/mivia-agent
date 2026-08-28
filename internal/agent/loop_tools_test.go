package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	appruntime "github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestToolEndDetailFailedBeatsTruncated(t *testing.T) {
	if got := toolEndDetail(toolExecResult{err: context.Canceled, truncated: true}); got != "failed (truncated)" {
		t.Fatalf("got %q", got)
	}
	if got := toolEndDetail(toolExecResult{err: context.Canceled}); got != "failed" {
		t.Fatalf("got %q", got)
	}
	if got := toolEndDetail(toolExecResult{truncated: true}); got != "completed (truncated)" {
		t.Fatalf("got %q", got)
	}
	if got := toolEndDetail(toolExecResult{}); got != "completed" {
		t.Fatalf("got %q", got)
	}
}

// toolCallNamed builds a result whose tool call carries name.
func toolCallNamed(name, body string) toolExecResult {
	var call provider.ToolCall
	call.Function.Name = name
	return toolExecResult{toolCall: call, result: body}
}

func TestToolEndDetailBodyHeuristicIsScopedToRunCommand(t *testing.T) {
	cases := []struct {
		name string
		tool string
		body string
		want string
	}{
		// run_command emits its exit status in the result header with err=nil.
		{"run_command exit 0", "run_command", "command: go test\ncwd: /w\nexit=0\nok", "completed"},
		{"run_command exit 1", "run_command", "command: go test\ncwd: /w\nexit=1\nFAIL", "failed"},
		{"run_command timeout", "run_command", "command: sleep\ncwd: /w\nexit=timeout\n", "failed"},
		{"run_command exit 01 is not success", "run_command", "command: x\ncwd: /w\nexit=01\n", "failed"},
		// Other tools return content verbatim; content is never a status signal.
		{"prose starting with Error", "read_file", "Error handling is important.\n", "completed"},
		{"prose starting with error:", "read_file", "error: codes are documented below\n", "completed"},
		{"content mentioning exit=", "read_file", "if [ $? -ne 0 ]; then exit=1; fi\n", "completed"},
		{"grep hit quoting exit=1", "search_files", "run.go:182: return \"exit=1\"\n", "completed"},
		{"ordinary content", "read_file", "package main\n", "completed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolEndDetail(toolCallNamed(tc.tool, tc.body)); got != tc.want {
				t.Fatalf("tool %q body %q: got %q want %q", tc.tool, tc.body, got, tc.want)
			}
		})
	}
}

// TestToolEndDetailDispatchTasksWholeBatchRejectionFails pins the
// dispatch_tasks whole-batch-failure envelope: Execute deliberately answers
// a pre-flight rejection (malformed wait value, expired caller context,
// coordinator/spawn failure) with {"error":...,"status":...} and a nil Go
// error, so the caller keeps the run_id/hint fields it needs to recover
// (internal/cliorchestrate/dispatch.go). Without a body check scoped to
// this tool, that envelope reads "completed" - the same class of bug
// run_command's own exit-code check exists to prevent, just for a
// different tool and a different signal shape. This is what left two
// sidebar rows reading "[completed] Elapsed: 0s, Tools: 0, Step: 0" for a
// batch that never dispatched a single task.
func TestToolEndDetailDispatchTasksWholeBatchRejectionFails(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"pre-flight wait rejection", `{"error":"unknown wait value \"maybe\""}`, "failed"},
		{"expired caller context", `{"error":"caller context already expired; no tasks were started","status":"canceled"}`, "failed"},
		{"run-level failure with hint", `{"error":"coordinator join failed","status":"failed","run_id":"run-1","hint":"inspect_agents or cancel_run can reach this run by run_id"}`, "failed"},
		{"invalid json with brace", "{not-json", "completed"},
		{"empty batch is not a failure", `{"tasks":[]}`, "completed"},
		{"per-task array result, tasks ran", `[{"task_id":"ba-core","status":"completed"}]`, "completed"},
		{"per-task array result, a task failed", `[{"task_id":"ba-core","status":"failed"}]`, "completed"},
		{"async payload always carries tasks", `{"run_id":"run-1","status":"running","tasks":[{"id":"ba-core","status":"running"}]}`, "completed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolEndDetail(toolCallNamed("dispatch_tasks", tc.body)); got != tc.want {
				t.Fatalf("body %q: got %q want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestToolEndDetailTransportErrorStillFails(t *testing.T) {
	// Synthesized "error: …" bodies always carry a non-nil err, so scoping the
	// body heuristic to run_command must not lose their failed status.
	r := toolCallNamed("read_file", "error: boom")
	r.err = context.Canceled
	if got := toolEndDetail(r); got != "failed" {
		t.Fatalf("got %q want failed", got)
	}
}

// TestToolEndDetailDuplicateLabels pins the operator row for duplicates: the
// failure signal is judged against the ORIGINAL recorded body the dedup cache
// served (a run_command duplicate reports its child exit in the recorded
// header with err==nil), never against the suppression notice that replaced
// it - which carries no status of its own. Constructs toolExecResult directly
// (the legacy buildExecResult construction path is gone with the legacy
// engine; the SDK dispatcher shim's equivalent construction is covered by
// sdk_duplicate_test.go).
func TestToolEndDetailDuplicateLabels(t *testing.T) {
	// exit=1 with err==nil: the recorded header is the only failure signal, and
	// the model-visible body must still be the notice.
	exec := toolExecResult{
		toolCall:     tc("call_dup_1", tools.RunCommandToolName, `{}`),
		duplicate:    true,
		originalBody: "command: go test\ncwd: /w\nexit=1\nFAIL",
		result:       duplicateDeliveryNotice,
	}
	if got := toolEndDetail(exec); got != "failed (duplicate)" {
		t.Fatalf("duplicate run_command exit=1: got %q, want %q", got, "failed (duplicate)")
	}

	// exit=0: the duplicate reads completed.
	exec = toolExecResult{
		toolCall:     tc("call_dup_1", tools.RunCommandToolName, `{}`),
		duplicate:    true,
		originalBody: "command: go test\ncwd: /w\nexit=0\nok",
		result:       duplicateDeliveryNotice,
	}
	if got := toolEndDetail(exec); got != "completed (duplicate)" {
		t.Fatalf("duplicate run_command exit=0: got %q, want %q", got, "completed (duplicate)")
	}

	// err != nil still fails a duplicate.
	failed := toolCallNamed("read_file", "whatever")
	failed.duplicate = true
	failed.err = context.Canceled
	if got := toolEndDetail(failed); got != "failed (duplicate)" {
		t.Fatalf("duplicate with err: got %q, want %q", got, "failed (duplicate)")
	}

	// A healthy non-run_command duplicate is completed.
	ok := toolCallNamed("read_file", "package main\n")
	ok.duplicate = true
	if got := toolEndDetail(ok); got != "completed (duplicate)" {
		t.Fatalf("duplicate healthy tool: got %q, want %q", got, "completed (duplicate)")
	}
}

func TestResolveToolCallTimeout_CapabilityCanExtendOrShorten(t *testing.T) {
	// Capability may grant more time than the default (long tools).
	if got := resolveToolCallTimeout(60*time.Second, 300*time.Second); got != 300*time.Second {
		t.Fatalf("extend: got %s want 300s", got)
	}
	// Capability may also tighten.
	if got := resolveToolCallTimeout(60*time.Second, 10*time.Millisecond); got != 10*time.Millisecond {
		t.Fatalf("shorten: got %s want 10ms", got)
	}
	// Missing capability falls back to default.
	if got := resolveToolCallTimeout(45*time.Second, 0); got != 45*time.Second {
		t.Fatalf("default: got %s want 45s", got)
	}
	// Non-positive default uses DefaultToolTimeout.
	if got := resolveToolCallTimeout(0, 0); got != DefaultToolTimeout {
		t.Fatalf("floor: got %s want %s", got, DefaultToolTimeout)
	}
}

func TestLoopRejectsDispatcherToolMissingFromVisibleRegistry(t *testing.T) {
	visible := tools.NewRegistry()
	full := tools.NewRegistry()
	privileged := &dispatcherOnlyTestTool{}
	full.Register(privileged)
	dispatcher, err := appruntime.NewToolDispatcher(full, appruntime.Policy{})
	if err != nil {
		t.Fatal(err)
	}

	loop := &Loop{
		Completer: &scriptCompleter{steps: []provider.Response{
			{ToolCalls: []provider.ToolCall{tc("privileged-call", privileged.Name(), `{}`)}, FinishReason: "tool_calls"},
			{Content: "done", FinishReason: "stop"},
		}},
		Tools: visible,
	}
	if _, err := loop.Run(context.Background(), "run", Options{Model: "m", MaxSteps: 3, Dispatcher: dispatcher}); err != nil {
		t.Fatal(err)
	}
	if privileged.executions.Load() != 0 {
		t.Fatalf("dispatcher executed tool absent from loop registry: executions=%d", privileged.executions.Load())
	}
}

type dispatcherOnlyTestTool struct{ executions atomic.Int32 }

func (*dispatcherOnlyTestTool) Name() string               { return "dispatcher_only" }
func (*dispatcherOnlyTestTool) Description() string        { return "test-only privileged tool" }
func (*dispatcherOnlyTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*dispatcherOnlyTestTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite}
}
func (t *dispatcherOnlyTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.executions.Add(1)
	return "executed", nil
}

func TestLoopToolTimeoutAndConflictSerialization(t *testing.T) {
	active := new(atomic.Int32)
	maxActive := new(atomic.Int32)
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "write", class: tools.ExecutionWrite, key: "path:same", delay: 100 * time.Millisecond, active: active, maxActive: maxActive})
	calls := []provider.ToolCall{tc("1", "write", `{}`), tc("2", "write", `{}`)}
	comp := &scriptCompleter{steps: []provider.Response{{ToolCalls: calls, FinishReason: "tool_calls"}, {Content: "done"}}}
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "run", Options{Model: "m", MaxSteps: 3, MaxConcurrentTools: 4})
	if err != nil {
		t.Fatal(err)
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("conflicting writes active=%d, want 1", got)
	}

	reg = tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "slow", class: tools.ExecutionRead, key: "path:slow", delay: time.Second})
	comp = &scriptCompleter{steps: []provider.Response{{ToolCalls: []provider.ToolCall{tc("1", "slow", `{}`)}, FinishReason: "tool_calls"}}}
	loop = &Loop{Completer: comp, Tools: reg}
	start := time.Now()
	_, err = loop.Run(context.Background(), "run", Options{Model: "m", MaxSteps: 2, ToolTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestToolLifecycleEventsExposeBoundedRedactedIO(t *testing.T) {
	installTestRedactionPolicy(t)
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "inspect", class: tools.ExecutionRead, key: "path:x", delay: time.Millisecond})
	comp := &scriptCompleter{steps: []provider.Response{
		{ToolCalls: []provider.ToolCall{tc("1", "inspect", `{"path":"x.txt","token":"do-not-leak"}`)}, FinishReason: "tool_calls"},
		{Content: "done"},
	}}
	var events []Event
	var mu sync.Mutex
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "inspect", Options{Model: "m", MaxSteps: 3, OnEvent: func(event Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}}); err != nil {
		t.Fatal(err)
	}
	var queued, running, end *Event
	for i := range events {
		if events[i].Kind == EventToolStart && events[i].Detail == "queued" {
			queued = &events[i]
		}
		if events[i].Kind == EventToolStart && events[i].Detail == "running" {
			running = &events[i]
		}
		if events[i].Kind == EventToolEnd {
			end = &events[i]
		}
	}
	if queued == nil || queued.ToolCallID != "1" || queued.Input == "" || !strings.Contains(queued.Input, "x.txt") || strings.Contains(queued.Input, "do-not-leak") {
		t.Fatalf("unexpected redacted input event: %+v", queued)
	}
	if running == nil || running.ToolCallID != "1" {
		t.Fatalf("expected running status event, got %+v", running)
	}
	if end == nil || end.ToolCallID != "1" || end.Output == "" || strings.Contains(end.Output, "do-not-leak") {
		t.Fatalf("unexpected output event: %+v", end)
	}
	if end.Detail != "completed" {
		t.Fatalf("end status=%q, want completed", end.Detail)
	}
}

func TestToolEndFiresPerToolBeforeBatchCompletes(t *testing.T) {
	// Fast tool finishes while slow tool still runs; End for fast must appear
	// before slow completes (not only after the whole batch).
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "fast", class: tools.ExecutionRead, key: "path:fast", delay: 5 * time.Millisecond})
	reg.Register(&scheduledTestTool{name: "slow", class: tools.ExecutionRead, key: "path:slow", delay: 150 * time.Millisecond})
	calls := []provider.ToolCall{tc("f", "fast", `{}`), tc("s", "slow", `{}`)}
	comp := &scriptCompleter{steps: []provider.Response{
		{ToolCalls: calls, FinishReason: "tool_calls"},
		{Content: "done"},
	}}
	var (
		mu     sync.Mutex
		order  []string
		ends   []time.Time
		starts = time.Now()
	)
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "batch", Options{Model: "m", MaxSteps: 3, MaxConcurrentTools: 2,
		OnEvent: func(e Event) {
			if e.Kind != EventToolEnd {
				return
			}
			mu.Lock()
			order = append(order, e.ToolCallID)
			ends = append(ends, time.Now())
			mu.Unlock()
		},
	}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 {
		t.Fatalf("ends=%v", order)
	}
	// Fast tool must end first (and well before slow's 150ms budget from start).
	if order[0] != "f" {
		t.Fatalf("first end=%q want f (per-tool end before batch wait)", order[0])
	}
	if ends[0].Sub(starts) > 80*time.Millisecond {
		t.Fatalf("fast tool end too late: %s (likely deferred until batch end)", ends[0].Sub(starts))
	}
}

func TestLoopToolResultBudgetIsExact(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "large", class: tools.ExecutionRead, delay: time.Millisecond})
	comp := &scriptCompleter{steps: []provider.Response{{ToolCalls: []provider.ToolCall{tc("1", "large", `{}`)}, FinishReason: "tool_calls"}, {Content: "done"}}}
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "run", Options{Model: "m", MaxSteps: 3, MaxToolResultChars: 5}); err != nil {
		t.Fatal(err)
	}
	for _, message := range loop.Messages {
		if message.Role == provider.RoleTool && len(message.Content) > 5 {
			t.Fatalf("tool result length=%d, want <=5", len(message.Content))
		}
	}
}
