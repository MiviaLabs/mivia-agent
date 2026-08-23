package agent

// Locks the Loop.LastFinishReason contract that the schema-repair loop relies
// on: after Run returns, LastFinishReason is the finish reason of the FINAL
// completed step — "stop" for a plain text reply, "length" for a reply the
// provider truncated against the output budget, the closing step's reason when
// a tool_calls step precedes the final text step, and "" when no step
// completed or when a fresh Run failed before any successful step.

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// failingCompleter errors on the very first ChatTurn, so a Run over it never
// completes a step.
type failingCompleter struct{ err error }

func (f failingCompleter) Name() string { return "failing" }
func (f failingCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", f.err
}
func (f failingCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", f.err
}
func (f failingCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return nil, f.err
}

func TestLoopLastFinishReasonSurfacesFinalStepReason(t *testing.T) {
	t.Run("plain text reply", func(t *testing.T) {
		loop := &Loop{
			Completer: &scriptCompleter{steps: []provider.Response{{Content: "answer", FinishReason: "stop"}}},
			Tools:     silentTurnRegistry(t),
		}
		if _, err := loop.Run(context.Background(), "go", Options{Backend: "legacy", Model: "m", MaxSteps: 5}); err != nil {
			t.Fatal(err)
		}
		if loop.LastFinishReason != "stop" {
			t.Fatalf("LastFinishReason = %q, want %q", loop.LastFinishReason, "stop")
		}
	})

	t.Run("truncated reply", func(t *testing.T) {
		loop := &Loop{
			Completer: &scriptCompleter{steps: []provider.Response{{Content: `{"ok":`, FinishReason: "length"}}},
			Tools:     silentTurnRegistry(t),
		}
		if _, err := loop.Run(context.Background(), "go", Options{Backend: "legacy", Model: "m", MaxSteps: 5}); err != nil {
			t.Fatalf("without RequireFinalText a truncated reply must stay successful: %v", err)
		}
		if loop.LastFinishReason != "length" {
			t.Fatalf("LastFinishReason = %q, want %q", loop.LastFinishReason, "length")
		}
	})

	t.Run("final step reason after tool_calls step", func(t *testing.T) {
		loop := &Loop{
			Completer: &scriptCompleter{steps: []provider.Response{
				{Content: "", FinishReason: "tool_calls",
					ToolCalls: []provider.ToolCall{tc("1", "read_file", `{"path":"x"}`)}},
				{Content: "answer", FinishReason: "stop"},
			}},
			Tools: silentTurnRegistry(t),
		}
		if _, err := loop.Run(context.Background(), "go", Options{Backend: "legacy", Model: "m", MaxSteps: 10}); err != nil {
			t.Fatal(err)
		}
		if loop.LastFinishReason != "stop" {
			t.Fatalf("LastFinishReason = %q, want the final step's %q", loop.LastFinishReason, "stop")
		}
	})

	t.Run("no completed step", func(t *testing.T) {
		loop := &Loop{
			Completer: failingCompleter{err: errors.New("provider down")},
			Tools:     silentTurnRegistry(t),
		}
		if _, err := loop.Run(context.Background(), "go", Options{Backend: "legacy", Model: "m", MaxSteps: 5}); err == nil {
			t.Fatal("expected the provider error")
		}
		if loop.LastFinishReason != "" {
			t.Fatalf("LastFinishReason = %q, want empty when no step completes", loop.LastFinishReason)
		}
	})

	t.Run("reset at the start of the next run", func(t *testing.T) {
		loop := &Loop{
			Completer: &scriptCompleter{steps: []provider.Response{{Content: "answer", FinishReason: "stop"}}},
			Tools:     silentTurnRegistry(t),
		}
		if _, err := loop.Run(context.Background(), "go", Options{Backend: "legacy", Model: "m", MaxSteps: 5}); err != nil {
			t.Fatal(err)
		}
		if loop.LastFinishReason != "stop" {
			t.Fatalf("LastFinishReason = %q, want %q", loop.LastFinishReason, "stop")
		}
		loop.Completer = failingCompleter{err: errors.New("provider down")}
		if _, err := loop.Run(context.Background(), "go", Options{Backend: "legacy", Model: "m", MaxSteps: 5}); err == nil {
			t.Fatal("expected the provider error")
		}
		if loop.LastFinishReason != "" {
			t.Fatalf("LastFinishReason = %q, want reset to empty on the next run", loop.LastFinishReason)
		}
	})
}
