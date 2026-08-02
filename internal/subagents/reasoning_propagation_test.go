package subagents

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// A nested subagent runs on a configured model just like the root session, so
// the model's reasoning dial has to reach its request too. Otherwise a
// delegated task quietly runs at a different reasoning depth than the task
// that spawned it.
func TestOneShotHandlerCarriesReasoning(t *testing.T) {
	comp := &captureRequestCompleter{}
	h := &OneShotHandler{
		Completer:    comp,
		Model:        "thinker",
		SystemPrompt: "analyze",
		Reasoning:    reasoning.Setting{Level: reasoning.High, Dialect: reasoning.DialectThinkingEffort},
	}
	if _, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "oneshot",
		Input: json.RawMessage(`"do the thing"`),
	}); err != nil {
		t.Fatal(err)
	}
	if comp.requests != 1 {
		t.Fatalf("requests = %d, want 1", comp.requests)
	}
	if comp.last.ReasoningLevel != reasoning.High || comp.last.ReasoningDialect != reasoning.DialectThinkingEffort {
		t.Fatalf("one-shot request carried %q/%q", comp.last.ReasoningLevel, comp.last.ReasoningDialect)
	}
}

func TestOneShotHandlerSendsNothingWhenUnset(t *testing.T) {
	comp := &captureRequestCompleter{}
	h := &OneShotHandler{Completer: comp, Model: "plain", SystemPrompt: "analyze"}
	if _, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "oneshot",
		Input: json.RawMessage(`"do the thing"`),
	}); err != nil {
		t.Fatal(err)
	}
	if comp.last.ReasoningLevel != "" || comp.last.ReasoningDialect != "" {
		t.Fatalf("unset handler carried %q/%q", comp.last.ReasoningLevel, comp.last.ReasoningDialect)
	}
}

// stepCaptureCompleter records every request the nested loop makes so the
// assertion covers each step, not only the first.
type stepCaptureCompleter struct {
	mu   sync.Mutex
	seen []provider.Request
}

func (c *stepCaptureCompleter) Name() string { return "step-capture" }

func (c *stepCaptureCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (c *stepCaptureCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	text, err := c.Chat(ctx, req)
	if err == nil && w != nil {
		_, _ = io.WriteString(w, text)
	}
	return text, err
}

func (c *stepCaptureCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.seen = append(c.seen, req)
	c.mu.Unlock()
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

func (c *stepCaptureCompleter) requests() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.seen...)
}

func TestMultiStepHandlerCarriesReasoning(t *testing.T) {
	comp := &stepCaptureCompleter{}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: newTestRegistry(),
		Model:        "thinker",
		SystemPrompt: "system",
		MaxSteps:     2,
		MaxTokens:    1024,
		Reasoning:    reasoning.Setting{Level: reasoning.Max, Dialect: reasoning.DialectOpenAI},
	}
	if _, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "multi_step",
		Input: json.RawMessage(`"analyze"`),
	}); err != nil {
		t.Fatal(err)
	}
	requests := comp.requests()
	if len(requests) == 0 {
		t.Fatal("no provider request was made")
	}
	for i, req := range requests {
		if req.ReasoningLevel != reasoning.Max || req.ReasoningDialect != reasoning.DialectOpenAI {
			t.Fatalf("step %d carried %q/%q", i, req.ReasoningLevel, req.ReasoningDialect)
		}
	}
}
