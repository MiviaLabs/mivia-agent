package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// recordingCompleter is a scripted completer that captures every request so
// tests can assert host history was replayed onto the next wire request.
type recordingCompleter struct {
	mu    sync.Mutex
	calls int
	steps []provider.Response
	reqs  []provider.Request
}

func (s *recordingCompleter) Name() string { return "recording" }
func (s *recordingCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := s.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (s *recordingCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return s.Chat(ctx, req)
}
func (s *recordingCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Copy messages so later mutations of loop.Messages do not rewrite history.
	cp := req
	cp.Messages = append([]provider.Message(nil), req.Messages...)
	s.reqs = append(s.reqs, cp)
	if s.calls >= len(s.steps) {
		return &provider.Response{Content: "done", FinishReason: "stop"}, nil
	}
	r := s.steps[s.calls]
	s.calls++
	if req.Stream && req.StreamWriter != nil && r.Content != "" {
		_, _ = io.WriteString(req.StreamWriter, r.Content)
	}
	return &r, nil
}

func TestLoopPersistsReasoningContentOnToolCallTurn(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "lookup", class: tools.ExecutionRead, key: "k"})
	comp := &recordingCompleter{
		steps: []provider.Response{
			{
				FinishReason:     "tool_calls",
				ReasoningContent: "I will look it up",
				ToolCalls:        []provider.ToolCall{tc("1", "lookup", `{}`)},
			},
			{Content: "found", FinishReason: "stop"},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "look up", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range loop.Messages {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			if m.ReasoningContent != "I will look it up" {
				t.Fatalf("tool-call assistant missing reasoning: %+v", m)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no tool-call assistant message in history")
	}
}

func TestLoopReplaysReasoningContentOnNextRequest(t *testing.T) {
	// The loop persists ReasoningContent generically; emission is the client's
	// job. Assert the *request* the completer sees on call 2 carries the field
	// on the assistant tool-call message (host history), which an adopting
	// client then puts on the wire.
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "lookup", class: tools.ExecutionRead, key: "k"})
	comp := &recordingCompleter{
		steps: []provider.Response{
			{
				FinishReason:     "tool_calls",
				ReasoningContent: "plan: call lookup",
				ToolCalls:        []provider.ToolCall{tc("1", "lookup", `{}`)},
			},
			{Content: "done", FinishReason: "stop", ReasoningContent: "wrap"},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "go", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	if len(comp.reqs) < 2 {
		t.Fatalf("expected >=2 requests, got %d", len(comp.reqs))
	}
	// Call 1 must not invent prior reasoning.
	for _, m := range comp.reqs[0].Messages {
		if m.ReasoningContent != "" {
			t.Fatalf("request 1 invented reasoning: %+v", m)
		}
	}
	// Call 2 must include the tool-call turn's reasoning verbatim.
	var saw string
	for _, m := range comp.reqs[1].Messages {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			saw = m.ReasoningContent
		}
	}
	if saw != "plan: call lookup" {
		t.Fatalf("request 2 missing replayed reasoning, got %q\nmessages=%+v", saw, comp.reqs[1].Messages)
	}
}

func TestLoopPersistsReasoningContentOnFinalAnswer(t *testing.T) {
	comp := &recordingCompleter{
		steps: []provider.Response{
			{Content: "final answer", FinishReason: "stop", ReasoningContent: "thinking final"},
		},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	if _, err := loop.Run(context.Background(), "say hi", Options{Model: "m", MaxSteps: 3}); err != nil {
		t.Fatal(err)
	}
	var got string
	for _, m := range loop.Messages {
		if m.Role == provider.RoleAssistant && m.Content == "final answer" {
			got = m.ReasoningContent
		}
	}
	if got != "thinking final" {
		t.Fatalf("final answer reasoning=%q", got)
	}
}

func TestNonReasoningModelNoReasoningContent(t *testing.T) {
	comp := &recordingCompleter{
		steps: []provider.Response{
			{Content: "plain", FinishReason: "stop"},
		},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	if _, err := loop.Run(context.Background(), "hi", Options{Model: "m", MaxSteps: 2}); err != nil {
		t.Fatal(err)
	}
	for _, m := range loop.Messages {
		if m.ReasoningContent != "" {
			t.Fatalf("unset ReasoningContent must stay empty, got %+v", m)
		}
	}
}

func TestEffortOffToolCallTurnDroppedForAdoptingProvider(t *testing.T) {
	// Edge: tool-call turn with empty reasoning (e.g. /effort off). The loop
	// stores the turn generically; an adopting client's emit path (toAPIMessages
	// via marshalBody) drops the exchange so a tools-carrying request never
	// ships a guaranteed-400 body. Drive the real shipped marshal path.
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "lookup", class: tools.ExecutionRead, key: "k"})
	comp := &recordingCompleter{
		steps: []provider.Response{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []provider.ToolCall{tc("1", "lookup", `{}`)},
			},
			{Content: "ok", FinishReason: "stop"},
		},
	}
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "go", Options{Model: "m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	var stored bool
	for _, m := range loop.Messages {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			if m.ReasoningContent != "" {
				t.Fatalf("effort-off turn should store empty reasoning, got %q", m.ReasoningContent)
			}
			stored = true
		}
	}
	if !stored {
		t.Fatal("expected tool-call turn stored in history")
	}

	// DeepSeek-shaped client: reject bit on → drop. z.ai-shaped: reject off → keep.
	deepseekWire := captureMarshalledMessages(t, true, true, loop.Messages)
	for _, m := range deepseekWire {
		if role, _ := m["role"].(string); role == provider.RoleAssistant {
			_, hasCalls := m["tool_calls"]
			_, hasReasoning := m["reasoning_content"]
			if hasCalls && !hasReasoning {
				t.Fatalf("DeepSeek emit must drop reasoning-less tool-call turn: %v", m)
			}
		}
		if id, _ := m["tool_call_id"].(string); id == "1" {
			t.Fatalf("tool result for dropped exchange must not reach wire: %v", m)
		}
	}
	zaiWire := captureMarshalledMessages(t, true, false, loop.Messages)
	var kept bool
	for _, m := range zaiWire {
		if _, ok := m["tool_calls"]; ok {
			kept = true
		}
	}
	if !kept {
		t.Fatal("z.ai emit must keep the reasoning-less tool-call exchange")
	}
}

// captureMarshalledMessages drives OpenAICompat.marshalBody (via ChatTurn against
// a stub) and returns the decoded messages array as the API would receive it.
func captureMarshalledMessages(t *testing.T, replay, reject bool, msgs []provider.Message) []map[string]any {
	t.Helper()
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	c := provider.NewOpenAICompatWithOptions(provider.CompatOptions{
		Name:                         "t",
		BaseURL:                      srv.URL,
		APIKey:                       "k",
		RequiresReasoningReplay:      replay,
		RejectReasoningLessToolTurns: reject,
	})
	if _, err := c.ChatTurn(context.Background(), provider.Request{
		Model:    "m",
		Messages: msgs,
		Tools:    []provider.ToolSpec{{"type": "function", "function": map[string]any{"name": "lookup"}}},
	}); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	return body.Messages
}
