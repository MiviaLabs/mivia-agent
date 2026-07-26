package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

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

	// Count events.
	startCount := 0
	endCount := 0
	parallelCount := 0
	for _, e := range events {
		switch e.Kind {
		case EventToolStart:
			startCount++
		case EventToolEnd:
			endCount++
		case EventToolParallel:
			parallelCount++
		}
	}
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
