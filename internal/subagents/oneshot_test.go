package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// mockCompleter implements provider.Completer for testing.
type mockCompleter struct {
	name     string
	response string
	err      error
}

func (m *mockCompleter) Name() string { return m.name }
func (m *mockCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}
func (m *mockCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return m.Chat(ctx, req)
}
func (m *mockCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	text, err := m.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return &provider.Response{Content: text}, nil
}

func TestOneShotHandlerInvoke(t *testing.T) {
	h := &OneShotHandler{
		Completer: &mockCompleter{
			name:     "test",
			response: "Found 3 API endpoints: /users, /auth, /health",
		},
		Model:        "test-model",
		SystemPrompt: "Analyze the codebase.",
	}

	result, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "test-task",
		Input: json.RawMessage(`"Find the API endpoints"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if parsed["output"] != "Found 3 API endpoints: /users, /auth, /health" {
		t.Fatalf("unexpected output: %v", parsed["output"])
	}
	if parsed["task"] != "Find the API endpoints" {
		t.Fatalf("unexpected task: %q", parsed["task"])
	}
}

func TestOneShotHandlerEmptyTask(t *testing.T) {
	h := &OneShotHandler{
		Completer:    &mockCompleter{name: "test", response: "ok"},
		Model:        "test-model",
		SystemPrompt: "Analyze.",
	}
	_, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "test",
		Input: json.RawMessage(`""`),
	})
	if err == nil {
		t.Fatal("expected error for empty task")
	}
}

func TestOneShotHandlerCancel(t *testing.T) {
	h := &OneShotHandler{
		Completer: &mockCompleter{
			name:     "test",
			response: "should not be reached",
		},
		Model:        "test-model",
		SystemPrompt: "Analyze.",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.Invoke(ctx, runtime.Request{
		Name:  "test",
		Input: json.RawMessage(`"task"`),
	})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestOneShotHandlerTimeout(t *testing.T) {
	h := &OneShotHandler{
		Completer: &mockCompleter{
			name:     "test",
			response: "ok",
		},
		Model:        "test-model",
		SystemPrompt: "Analyze.",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	// Wait for the context to expire deterministically.
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context should have expired")
	}
	_, err := h.Invoke(ctx, runtime.Request{
		Name:  "test",
		Input: json.RawMessage(`"task"`),
	})
	if err == nil {
		t.Fatal("expected error for timed-out context")
	}
}

func TestOneShotHandlerInvalidInput(t *testing.T) {
	h := &OneShotHandler{
		Completer:    &mockCompleter{name: "test", response: "ok"},
		Model:        "test-model",
		SystemPrompt: "Analyze.",
	}
	_, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "test",
		Input: json.RawMessage(`not-a-json-string`),
	})
	if err == nil {
		t.Fatal("expected error for invalid input")
	}
}

// Ensure OneShotHandler implements runtime.Handler at compile time.
var _ runtime.Handler = (*OneShotHandler)(nil)
