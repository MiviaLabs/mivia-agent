package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
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

func TestEmitModelThinkingHeartbeat_EmitsAtProgressCadenceAndStops(t *testing.T) {
	old := modelThinkingHeartbeatInterval
	modelThinkingHeartbeatInterval = 10 * time.Millisecond
	defer func() { modelThinkingHeartbeatInterval = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan Event, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		emitModelThinkingHeartbeat(ctx, Options{
			OnEvent: func(event Event) {
				events <- event
			},
		})
	}()

	select {
	case event := <-events:
		if event.Kind != EventHeartbeat {
			t.Fatalf("event kind = %q, want %q", event.Kind, EventHeartbeat)
		}
		if event.Detail != "working" {
			t.Fatalf("event detail = %q", event.Detail)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("model-thinking heartbeat did not arrive at the progress cadence")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("model-thinking heartbeat did not stop after cancellation")
	}
	select {
	case event := <-events:
		t.Fatalf("heartbeat emitted after cancellation: %+v", event)
	default:
	}
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
	// Simulate streaming content to FinalWriter when requested.
	if req.Stream && req.StreamWriter != nil && r.Content != "" {
		_, _ = io.WriteString(req.StreamWriter, r.Content)
	}
	return &r, nil
}

// revokeBuffer records writes and revoke calls for stream-revoke tests.
type revokeBuffer struct {
	strings.Builder
	revoked string
	revokeN int
}

func (r *revokeBuffer) RevokeStream() string {
	r.revokeN++
	r.revoked = r.String()
	r.Reset()
	return r.revoked
}

func TestLoopRevokesStreamOnToolCalls(t *testing.T) {
	// First step streams preamble then returns tool_calls - must revoke FinalWriter.
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{
		name: "read_a", class: tools.ExecutionRead, key: "path:a",
		delay: time.Millisecond,
	})
	comp := &scriptCompleter{
		steps: []provider.Response{
			{
				Content:      "I will read the file first…",
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("1", "read_a", `{"path":"a.txt"}`)},
			},
			{Content: "found it", FinishReason: "stop"},
		},
	}
	var fw revokeBuffer
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "read a", Options{Model: "m", MaxSteps: 5, FinalWriter: &fw})
	if err != nil {
		t.Fatal(err)
	}
	if fw.revokeN != 1 {
		t.Fatalf("expected RevokeStream once on tool step, got %d", fw.revokeN)
	}
	if !strings.Contains(fw.revoked, "I will read") {
		t.Fatalf("revoked text=%q", fw.revoked)
	}
	// Final answer should still land on FinalWriter after tools (streamed by scriptCompleter).
	if !strings.Contains(fw.String(), "found it") {
		t.Fatalf("final stream content=%q", fw.String())
	}
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
	var mu sync.Mutex
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "find Marker", Options{Model: "m",
		MaxSteps: 10,
		OnEvent: func(e Event) {
			// The SDK path synthesizes the legacy two-starts-per-call
			// wire shape ("queued" from the PointPreTool hook, then
			// "running" from the dispatcher shim - sdk_tool_events.go),
			// so 4 calls yield 8 starts.
			if e.Kind == EventToolStart {
				mu.Lock()
				toolStarts = append(toolStarts, e.Detail)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "found and noted" {
		t.Fatalf("text=%q", text)
	}
	if len(toolStarts) != 8 {
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
	// History should include a tool message reporting the failure, either as a
	// raw error body or as the dispatcher's bounded {"status":"failed"} envelope.
	found := false
	for _, m := range loop.Messages {
		if m.Role != provider.RoleTool {
			continue
		}
		if strings.Contains(m.Content, "error:") || strings.Contains(m.Content, `"status":"failed"`) {
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
	var mu sync.Mutex
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "read and search", Options{Model: "m",
		MaxSteps: 5,
		OnEvent: func(e Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "all files read and searched" {
		t.Fatalf("text=%q", text)
	}

	// The SDK path synthesizes the legacy two-starts-per-call wire
	// shape ("queued" + "running", sdk_tool_events.go) and runs the
	// batch sequentially (the documented MaxConcurrentTools gap), so
	// 3 calls yield 6 starts, 3 ends, and no tool_parallel event.
	starts, endCount, parallelCount := countToolEvents(events)
	if starts != 6 {
		t.Fatalf("expected 6 tool_start events (3 calls x queued+running), got %d", starts)
	}
	if endCount != 3 {
		t.Fatalf("expected 3 tool_end events, got %d", endCount)
	}
	if parallelCount != 0 {
		t.Fatalf("expected 0 tool_parallel events (sequential SDK execution), got %d", parallelCount)
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

func countToolLifecycle(events []Event) (queued, running, end, parallel int) {
	for _, event := range events {
		switch event.Kind {
		case EventToolStart:
			switch event.Detail {
			case "queued":
				queued++
			case "running":
				running++
			}
		case EventToolEnd:
			end++
		case EventToolParallel:
			parallel++
		}
	}
	return queued, running, end, parallel
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

	// Cancel almost immediately - tool calls should be interrupted.
	cancel()

	// Must return quickly (within a second) - not hang.
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
	installTestRedactionPolicy(t)
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
	var mu sync.Mutex
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "run", Options{Model: "m", MaxSteps: 3, MaxConcurrentTools: 2, OnEvent: func(e Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := maxActive.Load(); got > 2 {
		t.Fatalf("max active=%d, want <=2", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, event := range events {
		if strings.Contains(event.Detail, "hidden") || strings.Contains(event.Detail, "secret-result") {
			t.Fatalf("event leaked sensitive detail: %+v", event)
		}
		// Detail never carried the argv; the preview fields are where a
		// configured policy has to bite.
		if strings.Contains(event.Input, "hidden") || strings.Contains(event.Output, "hidden") {
			t.Fatalf("event preview leaked sensitive argv: %+v", event)
		}
	}
}

func TestLoopPublishesToEventBus(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{
		name: "read_a", class: tools.ExecutionRead, key: "path:a",
		delay: 5 * time.Millisecond,
	})
	comp := &scriptCompleter{
		steps: []provider.Response{
			{ToolCalls: []provider.ToolCall{tc("1", "read_a", `{"path":"a.txt"}`)}, FinishReason: "tool_calls"},
			{Content: "found it", FinishReason: "stop"},
		},
	}

	bus := events.New()
	var mu sync.Mutex
	var busEvents []events.Event
	// The SDK bridge carries the turn's tool lifecycle onto the bus
	// (EventToolStart/EventToolEnd); assistant deltas reach the bus only
	// through a FinalWriter tee, which this test does not wire.
	bus.Subscribe(events.KindToolStart, events.HandlerFunc(func(ctx context.Context, ev events.Event) {
		mu.Lock()
		busEvents = append(busEvents, ev)
		mu.Unlock()
	}))

	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "find files", Options{Model: "m",
		MaxSteps: 5,
		EventBus: bus,
		OnEvent:  func(e Event) {},
	})
	if err != nil {
		t.Fatal(err)
	}

	bus.Flush()
	mu.Lock()
	defer mu.Unlock()
	if len(busEvents) == 0 {
		t.Fatal("expected at least one event on the EventBus")
	}
	foundToolStart := false
	for _, ev := range busEvents {
		if ev.Kind == events.KindToolStart {
			foundToolStart = true
			break
		}
	}
	if !foundToolStart {
		t.Fatalf("expected at least one KindToolStart on bus, got %d events", len(busEvents))
	}
}

func TestLoopFallsBackToLastTextWhenFinalResponseEmpty(t *testing.T) {
	// When runStep returns done=true with empty text, the loop should fall
	// back to the last non-empty assistant text rather than returning "".
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	comp := &scriptCompleter{
		steps: []provider.Response{
			{
				Content:      "I found something relevant",
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("1", "read_file", `{"path":"x"}`)},
			},
			{Content: "I found something else", FinishReason: "tool_calls",
				ToolCalls: []provider.ToolCall{tc("2", "read_file", `{"path":"y"}`)},
			},
			// Final step: empty content with stop - this is the bug case.
			{Content: "", FinishReason: "stop"},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "investigate", Options{Model: "m", MaxSteps: 10})
	if err != nil {
		t.Fatal(err)
	}
	// Should have fallen back to "I found something else" (last non-empty text),
	// not returned empty string.
	if text == "" {
		t.Fatal("expected non-empty text fallback, got empty string")
	}
	if !strings.Contains(text, "found something else") {
		t.Fatalf("expected last non-empty text, got: %q", text)
	}
}

func TestLoopFallsBackToLastTextWhenFinalToolCallOnly(t *testing.T) {
	// Intermediate assistant text should survive a final empty-content response.
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	comp := &scriptCompleter{
		steps: []provider.Response{
			{
				Content:      "Setting up analysis",
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("1", "read_file", `{"path":"x"}`)},
			},
			{Content: "", FinishReason: "stop"},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	text, err := loop.Run(context.Background(), "analyze", Options{Model: "m", MaxSteps: 10})
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Fatal("expected non-empty text fallback, got empty string")
	}
	if !strings.Contains(text, "Setting up analysis") {
		t.Fatalf("expected fallback to prior text, got: %q", text)
	}
}

func TestLoopSkipsMalformedToolCallArguments(t *testing.T) {
	// A tool call with invalid JSON arguments must never be dispatched to the
	// tools registry. It is still announced in the assistant message and
	// paired with a bounded error result, because an
	// announced-but-unanswered call and a result answering no call are both
	// shapes the API and strict context planning reject.
	reg := tools.NewRegistry()
	comp := &scriptCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("1", "read_file", `{"path":`)},
			},
		},
	}

	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "read malformed", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}

	// Every announced call must have a paired error result, and the malformed
	// one must not have been dispatched. On the SDK path the documented
	// contract (sdk-backend-field-mapping.md) records malformed arguments
	// verbatim instead of normalizing them to "{}" — the paired result is
	// the tool layer's bounded error, not the legacy "call skipped" text.
	announced := map[string]bool{}
	answered := map[string]bool{}
	for _, m := range loop.Messages {
		if m.Role == provider.RoleTool {
			answered[m.ToolCallID] = true
			if m.ToolCallID == "1" && !strings.Contains(m.Content, "[tool-error]") {
				t.Fatalf("malformed-call error result not bounded: %q", m.Content)
			}
		}
		for _, c := range m.ToolCalls {
			announced[c.ID] = true
		}
	}
	if !announced["1"] {
		t.Fatalf("malformed call was not announced; messages: %+v", loop.Messages)
	}
	for id := range announced {
		if !answered[id] {
			t.Fatalf("tool call %q announced without a paired result", id)
		}
	}
	// The malformed call's bounded error result should be present in history.
	if !answered["1"] {
		t.Fatalf("expected a bounded error result for malformed call; messages: %+v", loop.Messages)
	}
}

// TestTeeWriterPublishesLiveDeltas: a cross-process observer (internal/hub's
// relay) only ever saw commitFinalAnswer's single aggregate EventAssistant,
// published once the whole reply was already generated - so an external view
// of a plain-text reply showed nothing while it streamed, then the whole
// thing at once. teeWriter must republish each Write as its own
// Detail="delta" EventAssistant, live, in addition to forwarding to the
// real writer unchanged.
func TestTeeWriterPublishesLiveDeltas(t *testing.T) {
	bus := events.New()
	var mu sync.Mutex
	var deltas []events.Event
	bus.Subscribe(events.KindAssistant, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		mu.Lock()
		defer mu.Unlock()
		deltas = append(deltas, ev)
	}))
	defer bus.Close()

	var forwarded strings.Builder
	tw := &teeWriter{w: &forwarded, opts: Options{
		EventBus: bus, SessionID: "sess-1", TurnID: "turn:1",
	}}

	if _, err := tw.Write([]byte("Hello, ")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tw.Write([]byte("world!")); err != nil {
		t.Fatalf("write: %v", err)
	}
	bus.Flush()

	if forwarded.String() != "Hello, world!" {
		t.Fatalf("real writer got %q, want the full forwarded text", forwarded.String())
	}
	if tw.String() != "Hello, world!" {
		t.Fatalf("teeWriter.String() = %q, want the buffered copy", tw.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deltas) != 2 {
		t.Fatalf("expected 2 delta events, got %d: %+v", len(deltas), deltas)
	}
	for i, want := range []string{"Hello, ", "world!"} {
		if deltas[i].Content != want || deltas[i].Detail != "delta" ||
			deltas[i].SessionID != "sess-1" || deltas[i].TurnID != "turn:1" {
			t.Fatalf("delta[%d] = %+v, want Content=%q Detail=delta SessionID=sess-1 TurnID=turn:1", i, deltas[i], want)
		}
	}
}

// TestTeeWriterSkipsEmptyWrites: an empty Write (a defensive no-op some
// writers issue) must not publish a meaningless empty delta.
func TestTeeWriterSkipsEmptyWrites(t *testing.T) {
	bus := events.New()
	var count atomic.Int32
	bus.Subscribe(events.KindAssistant, events.HandlerFunc(func(_ context.Context, _ events.Event) {
		count.Add(1)
	}))
	defer bus.Close()

	tw := &teeWriter{w: io.Discard, opts: Options{EventBus: bus, SessionID: "s", TurnID: "t"}}
	if _, err := tw.Write(nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	bus.Flush()
	if count.Load() != 0 {
		t.Fatalf("expected no delta published for an empty write, got %d", count.Load())
	}
}
