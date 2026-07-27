package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type scheduledTestTool struct {
	name      string
	class     tools.ExecutionClass
	key       string
	delay     time.Duration
	active    *atomic.Int32
	maxActive *atomic.Int32
	started   *atomic.Int32
}

func (t *scheduledTestTool) Name() string               { return t.name }
func (t *scheduledTestTool) Description() string        { return "test tool" }
func (t *scheduledTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *scheduledTestTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: t.class, ResourceKey: t.key}
}
func (t *scheduledTestTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	if t.started != nil {
		t.started.Add(1)
	}
	if t.active != nil {
		current := t.active.Add(1)
		for {
			old := t.maxActive.Load()
			if current <= old || t.maxActive.CompareAndSwap(old, current) {
				break
			}
		}
		defer t.active.Add(-1)
	}
	select {
	case <-time.After(t.delay):
		return "secret-result", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type scriptCompleter struct {
	calls int
	steps []provider.Response
}

func (s *scriptCompleter) Name() string { return "script" }
func (s *scriptCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := s.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (s *scriptCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return s.Chat(ctx, req)
}
func (s *scriptCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if s.calls >= len(s.steps) {
		return &provider.Response{Content: "done", FinishReason: "stop"}, nil
	}
	r := s.steps[s.calls]
	s.calls++
	return &r, nil
}

func tc(id, name, args string) provider.ToolCall {
	var c provider.ToolCall
	c.ID = id
	c.Type = "function"
	c.Function.Name = name
	c.Function.Arguments = args
	return c
}

func TestLoopWriteFile(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})

	comp := &scriptCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("1", "write_file", `{"path":"hi.txt","content":"hello"}`)},
			},
			{Content: "created hi.txt", FinishReason: "stop"},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "create hi.txt", Options{Model: "m", MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "created") {
		t.Fatalf("text=%q", text)
	}
	data, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"hi.txt"}`))
	if err != nil || data != "hello" {
		t.Fatalf("file=%q err=%v", data, err)
	}
}

func TestLoopNestedReadWriteAndGrep(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Seed nested tree.
	if err := os.MkdirAll(filepath.Join(dir, "internal", "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "foo", "bar.go"), []byte("package foo\nconst Marker = 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})

	// Simulate agent: list → grep → read nested file → write note → done
	comp := &scriptCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls: []provider.ToolCall{
					tc("1", "list_dir", `{"path":"internal"}`),
					tc("2", "grep", `{"pattern":"Marker","glob":"*.go"}`),
				},
			},
			{
				FinishReason: "tool_calls",
				ToolCalls: []provider.ToolCall{
					tc("3", "read_file", `{"path":"internal/foo/bar.go"}`),
					tc("4", "write_file", `{"path":"notes/out.txt","content":"found Marker"}`),
				},
			},
			{Content: "found and noted", FinishReason: "stop"},
		},
	}

	var toolStarts []string
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "find Marker", Options{
		Model:    "m",
		MaxSteps: 10,
		OnEvent: func(e Event) {
			if e.Kind == EventToolStart {
				toolStarts = append(toolStarts, e.Name)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "found and noted" {
		t.Fatalf("text=%q", text)
	}
	if len(toolStarts) != 4 {
		t.Fatalf("tool starts=%v", toolStarts)
	}
	note, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"notes/out.txt"}`))
	if err != nil || note != "found Marker" {
		t.Fatalf("note=%q err=%v", note, err)
	}
}

func TestLoopMaxSteps(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	// Always request another tool call.
	comp := &scriptCompleter{
		steps: []provider.Response{
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "list_dir", `{"path":"."}`)}},
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("2", "list_dir", `{"path":"."}`)}},
			{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("3", "list_dir", `{"path":"."}`)}},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	_, err = loop.Run(context.Background(), "loop", Options{Model: "m", MaxSteps: 2})
	if err == nil || !strings.Contains(err.Error(), "max_steps") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoopToolErrorReturnedToModel(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	comp := &scriptCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("1", "read_file", `{"path":"missing.txt"}`)},
			},
			{Content: "file missing handled", FinishReason: "stop"},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "read missing", Options{Model: "m", MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	if text != "file missing handled" {
		t.Fatalf("text=%q", text)
	}
	// History should include a tool message with error.
	found := false
	for _, m := range loop.Messages {
		if m.Role == provider.RoleTool && strings.Contains(m.Content, "error:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected tool error in history: %+v", loop.Messages)
	}
}

func TestLoopParallelToolExecution(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Seed a couple of files for concurrent reads.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("file-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("file-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})

	// Model responds with 3 tool calls in one turn.
	comp := &scriptCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls: []provider.ToolCall{
					tc("1", "read_file", `{"path":"a.txt"}`),
					tc("2", "read_file", `{"path":"b.txt"}`),
					tc("3", "grep", `{"pattern":"file","glob":"*.txt"}`),
				},
			},
			{Content: "all files read and searched", FinishReason: "stop"},
		},
	}

	var events []Event
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "read and search", Options{
		Model:    "m",
		MaxSteps: 5,
		OnEvent: func(e Event) {
			events = append(events, e)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "all files read and searched" {
		t.Fatalf("text=%q", text)
	}

	startCount, endCount, parallelCount := countToolEvents(events)
	if startCount != 3 {
		t.Fatalf("expected 3 tool_start events, got %d", startCount)
	}
	if endCount != 3 {
		t.Fatalf("expected 3 tool_end events, got %d", endCount)
	}
	if parallelCount != 1 {
		t.Fatalf("expected 1 tool_parallel event, got %d", parallelCount)
	}

	// Verify messages contain all 3 tool results in order.
	toolMsgs := 0
	for _, m := range loop.Messages {
		if m.Role == provider.RoleTool {
			toolMsgs++
		}
	}
	if toolMsgs != 3 {
		t.Fatalf("expected 3 tool messages in history, got %d", toolMsgs)
	}
}

func countToolEvents(events []Event) (start, end, parallel int) {
	for _, event := range events {
		switch event.Kind {
		case EventToolStart:
			start++
		case EventToolEnd:
			end++
		case EventToolParallel:
			parallel++
		}
	}
	return start, end, parallel
}

// TestLoopParallelCancellation verifies that when context is cancelled,
// all in-flight tool calls in parallel execution stop promptly.
func TestLoopParallelCancellation(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Write a large file to slow down read_file.
	data := strings.Repeat("x\n", 50000)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})

	comp := &scriptCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls: []provider.ToolCall{
					tc("1", "read_file", `{"path":"big.txt"}`),
					tc("2", "list_dir", `{"path":"."}`),
				},
			},
		},
	}

	// Create a cancellable context and cancel it immediately after tool calls fire.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		loop := &Loop{Completer: comp, Tools: reg}
		_, err := loop.Run(ctx, "do stuff", Options{Model: "m", MaxSteps: 5, MaxToolResultChars: 100})
		done <- err
	}()

	// Cancel almost immediately — tool calls should be interrupted.
	cancel()

	// Must return quickly (within a second) — not hang.
	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), context.Canceled.Error()) && err.Error() != "nil tools" {
			t.Fatalf("expected context.Canceled or nil, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: parallel tool calls did not respond to cancellation within 2s")
	}
}

func TestLoopToolConcurrencyLimitAndEventRedaction(t *testing.T) {
	active := new(atomic.Int32)
	maxActive := new(atomic.Int32)
	reg := tools.NewRegistry()
	for i := 0; i < 8; i++ {
		reg.Register(&scheduledTestTool{name: "read_" + string(rune('a'+i)), class: tools.ExecutionRead, key: "path:" + string(rune('a'+i)), delay: 30 * time.Millisecond, active: active, maxActive: maxActive})
	}
	var calls []provider.ToolCall
	for i := 0; i < 8; i++ {
		name := "read_" + string(rune('a'+i))
		calls = append(calls, tc(name, name, `{"path":"secret.txt","token":"hidden"}`))
	}
	comp := &scriptCompleter{steps: []provider.Response{{ToolCalls: calls, FinishReason: "tool_calls"}, {Content: "done"}}}
	var events []Event
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "run", Options{Model: "m", MaxSteps: 3, MaxConcurrentTools: 2, OnEvent: func(e Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	if got := maxActive.Load(); got > 2 {
		t.Fatalf("max active=%d, want <=2", got)
	}
	for _, event := range events {
		if strings.Contains(event.Detail, "hidden") || strings.Contains(event.Detail, "secret-result") {
			t.Fatalf("event leaked sensitive detail: %+v", event)
		}
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
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "inspect", class: tools.ExecutionRead, key: "path:x", delay: time.Millisecond})
	comp := &scriptCompleter{steps: []provider.Response{
		{ToolCalls: []provider.ToolCall{tc("1", "inspect", `{"path":"x.txt","token":"do-not-leak"}`)}, FinishReason: "tool_calls"},
		{Content: "done"},
	}}
	var events []Event
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "inspect", Options{Model: "m", MaxSteps: 3, OnEvent: func(event Event) { events = append(events, event) }}); err != nil {
		t.Fatal(err)
	}
	var start, end *Event
	for i := range events {
		if events[i].Kind == EventToolStart {
			start = &events[i]
		}
		if events[i].Kind == EventToolEnd {
			end = &events[i]
		}
	}
	if start == nil || start.ToolCallID != "1" || start.Input == "" || !strings.Contains(start.Input, "x.txt") || strings.Contains(start.Input, "do-not-leak") {
		t.Fatalf("unexpected redacted input event: %+v", start)
	}
	if start.Detail != "queued" {
		t.Fatalf("start status=%q, want queued", start.Detail)
	}
	if end == nil || end.ToolCallID != "1" || end.Output == "" || strings.Contains(end.Output, "do-not-leak") {
		t.Fatalf("unexpected output event: %+v", end)
	}
	if end.Detail != "completed" {
		t.Fatalf("end status=%q, want completed", end.Detail)
	}
}

func TestToolPreviewRedactionAndUTF8Bounds(t *testing.T) {
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
		if !strings.Contains(result.result, "deadline exceeded") {
			t.Fatalf("result[%d]=%q, want deadline error", i, result.result)
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
