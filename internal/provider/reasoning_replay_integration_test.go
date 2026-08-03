package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Multi-turn mock: adopting client replays request 1's reasoning on request 2
// with a stable prefix for cache-friendly tool turns.
func TestReasoningReplayIntegrationAdoptingPrefixStable(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, append([]byte(nil), raw...))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"step-one-plan","tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done","reasoning_content":"step-two-wrap"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "deepseek", BaseURL: srv.URL, APIKey: "k", RequiresReasoningReplay: true,
	})
	tools := []ToolSpec{{"type": "function", "function": map[string]any{"name": "lookup"}}}

	resp1, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "do work"}},
		Tools:    tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp1.ReasoningContent != "step-one-plan" {
		t.Fatalf("resp1.ReasoningContent=%q", resp1.ReasoningContent)
	}
	// Host history preserves reasoning (agent loop does this; we simulate).
	history := []Message{
		{Role: RoleUser, Content: "do work"},
		{
			Role:             RoleAssistant,
			ToolCalls:        resp1.ToolCalls,
			ReasoningContent: resp1.ReasoningContent,
		},
		{Role: RoleTool, ToolCallID: "c1", Name: "lookup", Content: "tool-result"},
	}
	if _, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Messages: history, Tools: tools,
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("bodies=%d", len(bodies))
	}
	assertNoAssistantReasoning(t, bodies[0])
	if !strings.Contains(string(bodies[1]), `"reasoning_content":"step-one-plan"`) {
		t.Fatalf("request 2 missing verbatim replay:\n%s", bodies[1])
	}
	reasons := assistantReasonings(t, bodies[1])
	if len(reasons) != 1 || reasons[0] != "step-one-plan" {
		t.Fatalf("prefix not stable: %v", reasons)
	}
}

// Non-adopting client: history with/without ReasoningContent must marshal
// identically (byte-stable request bodies).
func TestReasoningReplayIntegrationNonAdoptingByteIdentical(t *testing.T) {
	baseHistory := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
		{Role: RoleUser, Content: "next"},
	}
	withReasoning := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi", ReasoningContent: "should-not-leak"},
		{Role: RoleUser, Content: "next"},
	}
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "openrouter", BaseURL: "https://example.invalid/v1", APIKey: "k",
	})
	a, err := c.marshalBody(Request{Model: "m", Messages: baseHistory})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.marshalBody(Request{Model: "m", Messages: withReasoning})
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("non-adopting bodies differ:\n%s\n---\n%s", a, b)
	}
	if strings.Contains(string(a), "reasoning_content") {
		t.Fatalf("non-adopting body leaked reasoning_content: %s", a)
	}
}

// Legacy pre-plan session: reasoning-less tool-call exchange is dropped on
// adopting emit so a tools-carrying request is never a guaranteed 400.
func TestReasoningReplayIntegrationLegacyExchangeDropped(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "deepseek", BaseURL: srv.URL, APIKey: "k",
		RequiresReasoningReplay: true, RejectReasoningLessToolTurns: true,
	})
	legacy := []Message{
		{Role: RoleUser, Content: "read"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{toolCall("old", "lookup", `{}`)}},
		{Role: RoleTool, ToolCallID: "old", Name: "lookup", Content: "data"},
		{Role: RoleUser, Content: "and again"},
	}
	if _, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Messages: legacy,
		Tools:    []ToolSpec{{"type": "function", "function": map[string]any{"name": "lookup"}}},
	}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, m := range body.Messages {
		if _, hasCalls := m["tool_calls"]; hasCalls {
			t.Fatalf("legacy reasoning-less tool exchange must be dropped: %v", body.Messages)
		}
		if id, _ := m["tool_call_id"].(string); id == "old" {
			t.Fatalf("orphan tool result leaked: %v", m)
		}
	}
	if len(body.Messages) < 1 {
		t.Fatal("request emptied entirely")
	}
}

func assertNoAssistantReasoning(t *testing.T, raw []byte) {
	t.Helper()
	for _, r := range assistantReasonings(t, raw) {
		if r != "" {
			t.Fatalf("unexpected assistant reasoning in first request: %q body=%s", r, raw)
		}
	}
}

func assistantReasonings(t *testing.T, raw []byte) []string {
	t.Helper()
	var body struct {
		Messages []struct {
			Role             string `json:"role"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	var out []string
	for _, m := range body.Messages {
		if m.Role == RoleAssistant && m.ReasoningContent != "" {
			out = append(out, m.ReasoningContent)
		}
	}
	return out
}
