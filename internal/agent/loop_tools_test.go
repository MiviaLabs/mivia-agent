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
	"unicode/utf8"

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
	}, reg, 40*time.Millisecond)
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
		// plain tool delay is 80ms with 40ms budget — expect deadline
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

func TestRedactToolInputDefaultShowsArgs(t *testing.T) {
	tools.SetRedactToolArgs(false)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })
	raw := `{"path":"x.txt","token":"visible-when-off"}`
	got := redactToolInput(raw)
	if !strings.Contains(got, "visible-when-off") {
		t.Fatalf("default should show args: %q", got)
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
			if e.Kind == EventStep {
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
		t.Fatal("expected tool-batch heartbeat EventStep")
	}
	stop()
	stop() // idempotent
	mu.Lock()
	defer mu.Unlock()
	if len(steps) < 1 || !strings.Contains(steps[0], "tools ") || !strings.Contains(steps[0], "done") {
		t.Fatalf("heartbeat detail=%v", steps)
	}
}

func TestToolPreviewRedactionAndUTF8Bounds(t *testing.T) {
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })
	input := `{"path":"safe.txt","nested":{"token":"input-secret"},"content":"prompt-secret"}`
	gotInput := redactToolInput(input)
	if strings.Contains(gotInput, "input-secret") || strings.Contains(gotInput, "prompt-secret") {
		t.Fatalf("input leaked secret: %q", gotInput)
	}
	if !utf8.ValidString(gotInput) || len(gotInput) > 256 {
		t.Fatalf("input preview invalid/beyond cap: valid=%v len=%d", utf8.ValidString(gotInput), len(gotInput))
	}
	malformed := redactToolInput(`token=malformed-secret`)
	if strings.Contains(malformed, "malformed-secret") {
		t.Fatalf("malformed input leaked secret: %q", malformed)
	}
	providerKey := "sk-ant-" + strings.Repeat("a", 20)
	output := redactToolOutput("Authorization: Bearer bearer-secret " + providerKey + "\n" + strings.Repeat("界", 400))
	if strings.Contains(output, "bearer-secret") || strings.Contains(output, providerKey) {
		t.Fatalf("output leaked credential: %q", output)
	}
	if !utf8.ValidString(output) || len(output) > 512 {
		t.Fatalf("output preview invalid/beyond cap: valid=%v len=%d", utf8.ValidString(output), len(output))
	}
}

func TestToolPreviewRedaction_RemovesCompletePrivateKeyBlock(t *testing.T) {
	begin := strings.Join([]string{"-----BEGIN RSA", " PRIVATE KEY-----"}, "")
	end := strings.Join([]string{"-----END RSA", " PRIVATE KEY-----"}, "")
	output := begin + "\nopaque-body\n" + end
	got := redactToolOutputForTool("search_replace", output)
	if strings.Contains(got, "opaque-body") || strings.Contains(got, "BEGIN RSA") {
		t.Fatalf("private key material leaked: %q", got)
	}
	incomplete := strings.Join([]string{"-----BEGIN RSA", " PRIVATE KEY-----\ntruncated-body"}, "")
	if got := redactToolOutputForTool("search_replace", incomplete); strings.Contains(got, "truncated-body") {
		t.Fatalf("incomplete private key material leaked: %q", got)
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

func TestExecuteToolsParallel_EnforcesBatchCallAndResultBudgets(t *testing.T) {
	reg := tools.NewRegistry()
	for _, name := range []string{"one", "two", "three"} {
		reg.Register(&scheduledTestTool{name: name, class: tools.ExecutionRead, key: "path:" + name, delay: time.Millisecond})
	}
	calls := []provider.ToolCall{tc("1", "one", `{}`), tc("2", "two", `{}`), tc("3", "three", `{}`)}
	results := executeToolsParallel(context.Background(), calls, reg, Options{
		MaxConcurrentTools:      3,
		MaxToolCallsPerBatch:    2,
		MaxToolBatchResultChars: 10,
	})
	if len(results) != len(calls) {
		t.Fatalf("results=%d, want %d", len(results), len(calls))
	}
	if results[0].err != nil || len(results[0].result) != 10 {
		t.Fatalf("first result=%q err=%v, want bounded success", results[0].result, results[0].err)
	}
	if results[2].err == nil || !strings.Contains(results[2].err.Error(), "calls") {
		t.Fatalf("third result err=%v, want call budget error", results[2].err)
	}
	total := 0
	for _, result := range results {
		total += len(result.result)
	}
	if total > 10 {
		t.Fatalf("total result bytes=%d, want <=10", total)
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
		boundedTimeout := strings.Contains(result.result, `"status":"timed_out"`) && strings.Contains(result.result, `"error_ref":"ref:error:`)
		legacyTimeout := strings.Contains(result.result, "deadline exceeded")
		if !boundedTimeout && !legacyTimeout {
			t.Fatalf("result[%d]=%q, want bounded timed_out error envelope", i, result.result)
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
