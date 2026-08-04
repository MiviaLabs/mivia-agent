package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
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
// it - which carries no status of its own.
func TestToolEndDetailDuplicateLabels(t *testing.T) {
	reg := tools.NewRegistry()
	task := &toolTask{call: tc("call_dup_1", tools.RunCommandToolName, `{}`)}

	// exit=1 with err==nil: the recorded header is the only failure signal, and
	// the model-visible body must still be the notice.
	exec := buildExecResult(0, task, reg, Options{}, appruntime.Result{
		Output:   []byte("command: go test\ncwd: /w\nexit=1\nFAIL"),
		Metadata: appruntime.Metadata{Status: "duplicate"},
	})
	if !exec.duplicate {
		t.Fatal("duplicate result lost the duplicate flag")
	}
	if !strings.Contains(exec.result, EXPECTED_NOTICE) {
		t.Fatalf("duplicate result = %q, want the notice %q", exec.result, EXPECTED_NOTICE)
	}
	if got := toolEndDetail(exec); got != "failed (duplicate)" {
		t.Fatalf("duplicate run_command exit=1: got %q, want %q", got, "failed (duplicate)")
	}

	// exit=0: the duplicate reads completed.
	exec = buildExecResult(0, task, reg, Options{}, appruntime.Result{
		Output:   []byte("command: go test\ncwd: /w\nexit=0\nok"),
		Metadata: appruntime.Metadata{Status: "duplicate"},
	})
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

func TestPrepareToolTasks_CapabilityTimeoutExtendsBeyondDefault(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&capTimeoutTool{name: "long_tool", timeout: 200 * time.Millisecond})
	reg.Register(&capTimeoutTool{name: "plain_tool", timeout: 0})

	start := time.Now()
	ctx := context.Background()
	tasks := prepareToolTasks(ctx, []provider.ToolCall{
		tc("1", "long_tool", `{}`),
		tc("2", "plain_tool", `{}`),
	}, reg, 40*time.Millisecond, 0)
	defer func() {
		for _, task := range tasks {
			task.cancel()
		}
	}()

	if tasks[0].timeout != 200*time.Millisecond {
		t.Fatalf("long_tool timeout=%s, want 200ms (capability extends)", tasks[0].timeout)
	}
	if tasks[1].timeout != 40*time.Millisecond {
		t.Fatalf("plain_tool timeout=%s, want 40ms (default)", tasks[1].timeout)
	}

	// Drive real execution: long tool should complete under extended budget;
	// plain tool with 40ms budget and 80ms work should deadline.
	results := executeToolsParallel(ctx, []provider.ToolCall{
		tc("1", "long_tool", `{}`),
		tc("2", "plain_tool", `{}`),
	}, reg, Options{ToolTimeout: 40 * time.Millisecond, MaxConcurrentTools: 2})
	if elapsed := time.Since(start); elapsed > 800*time.Millisecond {
		t.Fatalf("execution hung: %s", elapsed)
	}
	if results[0].err != nil {
		t.Fatalf("long_tool should succeed under capability budget: %v body=%q", results[0].err, results[0].result)
	}
	if results[1].err == nil && !strings.Contains(results[1].result, "deadline") {
		// plain tool delay is 80ms with 40ms budget - expect deadline
		t.Fatalf("plain_tool expected timeout, got err=%v result=%q", results[1].err, results[1].result)
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

// capTimeoutTool delays; when timeout capability is set, Execute sleeps
// slightly less than that budget so an extended budget succeeds.
type capTimeoutTool struct {
	name    string
	timeout time.Duration
}

func (t *capTimeoutTool) Name() string               { return t.name }
func (t *capTimeoutTool) Description() string        { return "cap timeout test" }
func (t *capTimeoutTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *capTimeoutTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, Timeout: t.timeout, ResourceKey: "path:" + t.name}
}
func (t *capTimeoutTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	// Work duration: if capability timeout is set, use half of it; else 80ms
	// which exceeds the 40ms default in the test above.
	work := 80 * time.Millisecond
	if t.timeout > 0 {
		work = t.timeout / 2
	}
	select {
	case <-time.After(work):
		return "ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
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

func TestExecuteToolsParallel_QueuedToolTimeoutStartsAfterDequeue(t *testing.T) {
	// Five calls but four workers: the fifth waits on the jobs channel. Its
	// per-call budget must start when a worker picks it up, not at batch
	// preparation - otherwise a queued tool expires without ever executing.
	started := new(atomic.Int32)
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "slow", class: tools.ExecutionRead, delay: 300 * time.Millisecond})
	reg.Register(&scheduledTestTool{name: "quick", class: tools.ExecutionRead, delay: time.Millisecond, started: started})
	calls := []provider.ToolCall{
		tc("1", "slow", `{}`),
		tc("2", "slow", `{}`),
		tc("3", "slow", `{}`),
		tc("4", "slow", `{}`),
		tc("5", "quick", `{}`),
	}

	results := executeToolsParallel(context.Background(), calls, reg, Options{
		MaxConcurrentTools: 4,
		ToolTimeout:        200 * time.Millisecond,
	})

	if len(results) != len(calls) {
		t.Fatalf("results=%d, want %d", len(results), len(calls))
	}
	if got := started.Load(); got != 1 {
		t.Fatalf("queued tool executions=%d, want 1 (budget burned while queued)", got)
	}
	if results[4].err != nil {
		t.Fatalf("queued tool failed: %v result=%q", results[4].err, results[4].result)
	}
	if results[4].toolCall.ID != "5" {
		t.Fatalf("result[4].id=%q, want 5", results[4].toolCall.ID)
	}
}

func TestExecuteToolsParallel_ResourceLockWaitDoesNotConsumeCallBudget(t *testing.T) {
	// Same resource key serializes the two calls; the second one waits inside
	// scheduler.acquire. That wait must not be charged against its own budget.
	started := new(atomic.Int32)
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{
		name: "write", class: tools.ExecutionWrite, key: "path:same",
		delay: 60 * time.Millisecond, started: started,
	})
	calls := []provider.ToolCall{tc("1", "write", `{}`), tc("2", "write", `{}`)}

	results := executeToolsParallel(context.Background(), calls, reg, Options{
		MaxConcurrentTools: 4,
		ToolTimeout:        100 * time.Millisecond,
	})

	if got := started.Load(); got != 2 {
		t.Fatalf("executions=%d, want 2 (lock wait burned the second budget)", got)
	}
	for i, result := range results {
		if result.err != nil {
			t.Fatalf("result[%d] failed: %v body=%q", i, result.err, result.result)
		}
	}
}

func TestToolSchedulerCanceledAcquireDoesNotLeakKeyLock(t *testing.T) {
	// A waiter that leaves through ctx.Done() must apply the same cleanup as
	// the release path. Resource keys are per file path, so a leaked entry per
	// canceled call grows the map without bound over a long session.
	scheduler := newToolScheduler(4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Each iteration races the cancel against both selects in acquire, so a
	// single pass would be flaky; the aggregate over many distinct keys is not.
	for i := 0; i < 500; i++ {
		release, err := scheduler.acquire(ctx, fmt.Sprintf("path:leak-%03d", i))
		if err == nil {
			release()
		}
	}
	scheduler.mu.Lock()
	leaked := len(scheduler.locks)
	scheduler.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("keyLock entries leaked after canceled acquires: %d", leaked)
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
	if _, err := loop.Run(context.Background(), "batch", Options{
		Model: "m", MaxSteps: 3, MaxConcurrentTools: 2,
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

func TestToolBatchHeartbeatEmitsWhileToolsRun(t *testing.T) {
	old := toolBatchHeartbeatInterval
	toolBatchHeartbeatInterval = 25 * time.Millisecond
	defer func() { toolBatchHeartbeatInterval = old }()

	var (
		mu     sync.Mutex
		steps  []string
		finish atomic.Int32
	)
	gotStep := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startToolBatchHeartbeat(ctx, Options{
		OnEvent: func(e Event) {
			if e.Kind == EventHeartbeat {
				mu.Lock()
				steps = append(steps, e.Detail)
				mu.Unlock()
				select {
				case gotStep <- struct{}{}:
				default:
				}
			}
		},
	}, 2, 2, &finish)
	finish.Store(1)
	select {
	case <-gotStep:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected tool-batch heartbeat EventHeartbeat")
	}
	stop()
	stop() // idempotent
	mu.Lock()
	defer mu.Unlock()
	if len(steps) < 1 || !strings.Contains(steps[0], "tools ") || !strings.Contains(steps[0], "done") {
		t.Fatalf("heartbeat detail=%v", steps)
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

func TestExecuteToolsParallel_EnforcesBatchCallBudget(t *testing.T) {
	reg := tools.NewRegistry()
	for _, name := range []string{"one", "two", "three"} {
		reg.Register(&scheduledTestTool{name: name, class: tools.ExecutionRead, key: "path:" + name, delay: time.Millisecond})
	}
	calls := []provider.ToolCall{tc("1", "one", `{}`), tc("2", "two", `{}`), tc("3", "three", `{}`)}
	results := executeToolsParallel(context.Background(), calls, reg, Options{
		MaxConcurrentTools:   3,
		MaxToolCallsPerBatch: 2,
	})
	if len(results) != len(calls) {
		t.Fatalf("results=%d, want %d", len(results), len(calls))
	}
	if results[2].err == nil || !strings.Contains(results[2].err.Error(), "calls") {
		t.Fatalf("third result err=%v, want call budget error", results[2].err)
	}
	// The call-count budget bounds how many tools EXECUTE. It must never zero
	// or shrink the content of results that did execute - the only per-result
	// byte bound is capToolResult (MaxToolResultChars / Capability budgets).
	for i := 0; i < 2; i++ {
		if results[i].err != nil {
			t.Fatalf("result %d err=%v, want success", i, results[i].err)
		}
		if results[i].result == "" || results[i].truncated {
			t.Fatalf("result %d content=%q truncated=%v; batch ceiling must not touch executed results",
				i, results[i].result, results[i].truncated)
		}
	}
}

func TestExecuteToolsParallel_QueueSaturationIncludesTimeoutAndPreservesOrder(t *testing.T) {
	started := new(atomic.Int32)
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{
		name:    "slow",
		class:   tools.ExecutionRead,
		key:     "same",
		delay:   time.Second,
		started: started,
	})
	calls := []provider.ToolCall{
		tc("first", "slow", `{}`),
		tc("second", "slow", `{}`),
	}

	start := time.Now()
	results := executeToolsParallel(context.Background(), calls, reg, Options{
		MaxConcurrentTools: 1,
		ToolTimeout:        25 * time.Millisecond,
	})

	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("queue-saturated execution took %s", elapsed)
	}
	if len(results) != len(calls) {
		t.Fatalf("results=%d, want %d", len(results), len(calls))
	}
	if got := started.Load(); got < 1 {
		t.Fatalf("started=%d, want at least the first call to start", got)
	}
	for i, result := range results {
		if result.index != i {
			t.Fatalf("result[%d].index=%d, want %d", i, result.index, i)
		}
		if result.toolCall.ID != calls[i].ID {
			t.Fatalf("result[%d].id=%q, want %q", i, result.toolCall.ID, calls[i].ID)
		}
		if result.err == nil {
			t.Fatalf("result[%d] unexpectedly succeeded", i)
		}
		// Two distinct paths can time a call out. A call that reached the
		// dispatcher comes back as the bounded envelope: a status and nothing
		// else, with no raw "deadline exceeded" body and no reference (nothing at
		// that layer stores content, so no reference may be minted there). A call
		// killed before the dispatcher - waiting on the resource lock in
		// scheduler.acquire, or already past its deadline at the pre-exec check -
		// is reported by executeToolTask as the raw context error instead.
		boundedTimeout := strings.Contains(result.result, `"status":"timed_out"`) &&
			!strings.Contains(result.result, "deadline exceeded") &&
			!strings.Contains(result.result, "ref:")
		legacyTimeout := strings.Contains(result.result, "deadline exceeded")
		if !boundedTimeout && !legacyTimeout {
			t.Fatalf("result[%d]=%q, want bounded timed_out envelope or raw context error", i, result.result)
		}
	}
}

func TestExecuteToolsParallel_CancellationStopsQueuedProducer(t *testing.T) {
	started := new(atomic.Int32)
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{
		name:    "blocking",
		class:   tools.ExecutionRead,
		delay:   time.Hour,
		started: started,
	})
	calls := make([]provider.ToolCall, 64)
	for i := range calls {
		calls[i] = tc(string(rune('a'+i)), "blocking", `{}`)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []toolExecResult, 1)
	go func() {
		done <- executeToolsParallel(ctx, calls, reg, Options{MaxConcurrentTools: 2})
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for started.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("blocking tool did not start")
		default:
			runtime.Gosched()
		}
	}
	cancel()

	select {
	case results := <-done:
		if len(results) != len(calls) {
			t.Fatalf("results=%d, want %d", len(results), len(calls))
		}
		for i, result := range results {
			if result.index != i {
				t.Fatalf("result[%d].index=%d, want %d", i, result.index, i)
			}
			if result.err == nil {
				t.Fatalf("result[%d] unexpectedly succeeded", i)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation left the producer or workers blocked")
	}
}

func TestExecuteToolsParallel_StressBoundAndDeterministicOrder(t *testing.T) {
	active := new(atomic.Int32)
	maxActive := new(atomic.Int32)
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{
		name: "stress", class: tools.ExecutionRead, key: "",
		delay: 2 * time.Millisecond, active: active, maxActive: maxActive,
	})
	calls := make([]provider.ToolCall, 32)
	for i := range calls {
		calls[i] = tc(fmt.Sprintf("stress-%02d", i), "stress", `{}`)
	}
	results := executeToolsParallel(context.Background(), calls, reg, Options{MaxConcurrentTools: 3})
	if got := maxActive.Load(); got > 3 {
		t.Fatalf("max active=%d, want <=3", got)
	}
	for i, result := range results {
		if result.index != i || result.toolCall.ID != calls[i].ID {
			t.Fatalf("result[%d] identity=(%d,%q), want (%d,%q)", i, result.index, result.toolCall.ID, i, calls[i].ID)
		}
		if result.err != nil {
			t.Fatalf("result[%d] error: %v", i, result.err)
		}
	}
}

// TestExecuteToolsParallelReadClassIdenticalCallsBothExecute pins the Wave B
// loop contract: the loop stamps SkipDedup on ExecutionRead-class tool calls,
// so two IDENTICAL read-class calls in one batch (fresh call IDs, same
// arguments) both reach their handler and execute fresh.
func TestExecuteToolsParallelReadClassIdenticalCallsBothExecute(t *testing.T) {
	var readCalls atomic.Int32
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{
		name: "count_read", class: tools.ExecutionRead, key: "path:count-read",
		delay: time.Millisecond, started: &readCalls,
	})
	dispatcher, err := appruntime.NewToolDispatcher(reg, appruntime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()

	readArgs := `{"path":"x.txt"}`
	// One batch of TWO identical read-class calls: fresh IDs, same arguments.
	// Both must execute fresh (SkipDedup is stamped for ExecutionRead tools).
	readResults := executeToolsParallel(context.Background(), []provider.ToolCall{
		tc("read-1", "count_read", readArgs),
		tc("read-2", "count_read", readArgs),
	}, reg, Options{
		TurnID: "turn:1", ParentID: "session", Step: 1,
		Dispatcher: dispatcher, MaxConcurrentTools: 4,
	})
	for i, result := range readResults {
		if result.err != nil {
			t.Fatalf("read result[%d] err=%v body=%q", i, result.err, result.result)
		}
	}
	if got := readCalls.Load(); got != 2 {
		t.Fatalf("read handler executed %d times, want 2 (identical read-class calls must both execute fresh)", got)
	}
}

// TestExecuteToolsParallelWriteClassDedupStays pins the write-class control:
// identical write-class calls keep the per-turn dedup, so one executes and the
// other is served from the recorded result. Owner/duplicate assignment is a
// worker race, so the assertions are order-agnostic counts (Wave C: the
// duplicate carries the suppression notice, never the owner's body).
func TestExecuteToolsParallelWriteClassDedupStays(t *testing.T) {
	var writeCalls atomic.Int32
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{
		name: "count_write", class: tools.ExecutionWrite, key: "path:count-write",
		delay: time.Millisecond, started: &writeCalls,
	})
	dispatcher, err := appruntime.NewToolDispatcher(reg, appruntime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()

	writeArgs := `{"path":"x.txt","content":"hello"}`
	writeResults := executeToolsParallel(context.Background(), []provider.ToolCall{
		tc("write-1", "count_write", writeArgs),
		tc("write-2", "count_write", writeArgs),
	}, reg, Options{
		TurnID: "turn:1", ParentID: "session", Step: 1,
		Dispatcher: dispatcher, MaxConcurrentTools: 4,
	})
	if got := writeCalls.Load(); got != 1 {
		t.Fatalf("write handler executed %d times, want 1 (write-class dedup must stay)", got)
	}
	marked, full := 0, 0
	for _, r := range writeResults {
		if r.duplicate {
			marked++
			if !strings.Contains(r.result, EXPECTED_NOTICE) {
				t.Fatalf("duplicate write result = %q, want the suppression notice", r.result)
			}
		} else {
			full++
		}
	}
	if marked != 1 || full != 1 {
		t.Fatalf("write batch: marked=%d full=%d, want exactly one of each", marked, full)
	}
	// toolExecResult carries no runtime.Metadata, so the duplicate status is
	// asserted at the runtime layer: a third identical call answers from the
	// turn bucket as duplicate without executing.
	dup := dispatcher.Invoke(context.Background(), appruntime.Request{
		ID: "write-3", Kind: appruntime.Tool, Name: "count_write",
		Input: json.RawMessage(writeArgs), TurnID: "turn:1", ParentID: "session", Step: 1,
	})
	if dup.Err != nil {
		t.Fatalf("third write call err=%v", dup.Err)
	}
	if dup.Metadata.Status != "duplicate" {
		t.Fatalf("third write call status = %q, want duplicate", dup.Metadata.Status)
	}
	if got := writeCalls.Load(); got != 1 {
		t.Fatalf("write handler executed %d times after the duplicate call, want 1 (the recorded result must serve it)", got)
	}
}
