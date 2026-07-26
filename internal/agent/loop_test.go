package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
