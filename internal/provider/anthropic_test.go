package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func newTestAnthropicClient(t *testing.T, handler http.HandlerFunc) *AnthropicCompleter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	comp, err := NewAnthropic(Options{BaseURL: srv.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	client, ok := comp.(*AnthropicCompleter)
	if !ok {
		t.Fatalf("NewAnthropic returned %T, want *AnthropicCompleter", comp)
	}
	return client
}

// A plain text turn: the required headers reach the wire, the request body
// carries the OpenAI-shaped Request translated into Anthropic's system +
// messages shape, and the response's single text block becomes Content with
// FinishReason "stop" (Anthropic's end_turn).
func TestAnthropicChatTurnPlainText(t *testing.T) {
	var gotHeaders http.Header
	var gotBody map[string]any
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hello there"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3}}`))
	})

	req := Request{
		Model: "claude-sonnet-5",
		Messages: []Message{
			{Role: RoleSystem, Content: "You are mivia."},
			{Role: RoleUser, Content: "hi"},
		},
	}
	resp, err := client.ChatTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.Content != "hello there" {
		t.Fatalf("Content = %q, want %q", resp.Content, "hello there")
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
	if !resp.TokenUsage.Reported || resp.TokenUsage.InputTokens != 10 || resp.TokenUsage.OutputTokens != 3 {
		t.Fatalf("TokenUsage = %+v, want Reported input=10 output=3", resp.TokenUsage)
	}

	if got := gotHeaders.Get("anthropic-version"); got != anthropicAPIVersion {
		t.Fatalf("anthropic-version header = %q, want %q", got, anthropicAPIVersion)
	}
	if got := gotHeaders.Get("x-api-key"); got != "test-key" {
		t.Fatalf("x-api-key header = %q, want %q", got, "test-key")
	}
	if got, _ := gotBody["system"].(string); got != "You are mivia." {
		t.Fatalf("system = %q, want %q", got, "You are mivia.")
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want exactly one user turn", gotBody["messages"])
	}
}

// A parallel tool-call turn produces N consecutive RoleTool Messages in this
// codebase's history (mivia-ai-sdk/agentloop/toolcall.go's runToolCalls
// appends one per call). Anthropic requires them coalesced into a single
// role:"user" message carrying one tool_result block per call, or the
// request 400s on role alternation. This is the fix for the confirmed bug
// the correctness-review agent found in Step 0 before this file existed.
func TestAnthropicCoalescesParallelToolResultsIntoOneUserTurn(t *testing.T) {
	var gotBody map[string]any
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	})

	req := Request{
		Model: "claude-sonnet-5",
		Messages: []Message{
			{Role: RoleUser, Content: "read two files"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				newToolCall("toolu_1", "read_file", `{"path":"a.go"}`),
				newToolCall("toolu_2", "read_file", `{"path":"b.go"}`),
			}},
			{Role: RoleTool, ToolCallID: "toolu_1", Content: "package a"},
			{Role: RoleTool, ToolCallID: "toolu_2", Content: "package b"},
		},
	}
	if _, err := client.ChatTurn(context.Background(), req); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}

	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d entries, want 3 (user, assistant, coalesced tool-result user); got %#v", len(msgs), gotBody["messages"])
	}
	last, _ := msgs[2].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("last message role = %v, want user", last["role"])
	}
	content, _ := last["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("coalesced tool-result content = %d blocks, want 2 (one per parallel call), got %#v", len(content), last["content"])
	}
	for i, want := range []string{"toolu_1", "toolu_2"} {
		block, _ := content[i].(map[string]any)
		if block["type"] != "tool_result" || block["tool_use_id"] != want {
			t.Fatalf("content[%d] = %#v, want tool_result for %s", i, block, want)
		}
	}
}

// A tool-result run immediately followed by an injected/user notice message
// (e.g. internal/agent/agentloop_run.go's promptTooLongCompactNotice, sent as
// RoleUser) must also coalesce: both map to Anthropic role "user", and two
// consecutive Anthropic "user" messages would violate strict alternation the
// same way two consecutive tool-result messages would.
func TestAnthropicCoalescesToolResultFollowedByUserNotice(t *testing.T) {
	var gotBody map[string]any
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	})
	req := Request{
		Model: "claude-sonnet-5",
		Messages: []Message{
			{Role: RoleUser, Content: "go"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{newToolCall("toolu_1", "read_file", `{"path":"a.go"}`)}},
			{Role: RoleTool, ToolCallID: "toolu_1", Content: "package a"},
			{Role: RoleUser, Content: "[context compacted]"},
		},
	}
	if _, err := client.ChatTurn(context.Background(), req); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d entries, want 3 (user, assistant, coalesced tool-result+notice user); got %#v", len(msgs), gotBody["messages"])
	}
	last, _ := msgs[2].(map[string]any)
	content, _ := last["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("coalesced content = %d blocks, want 2 (tool_result then text notice), got %#v", len(content), content)
	}
	if block, _ := content[0].(map[string]any); block["type"] != "tool_result" {
		t.Fatalf("content[0] = %#v, want tool_result", block)
	}
	if block, _ := content[1].(map[string]any); block["type"] != "text" || block["text"] != "[context compacted]" {
		t.Fatalf("content[1] = %#v, want the notice text block", block)
	}
}

// A genuinely empty assistant turn (no content, no tool calls - the shape a
// provider's empty response leaves behind) between two user-mapped messages
// must not cause two adjacent Anthropic "user" messages. Confirmed bug from
// the Step-5 hostile audit: anthropicSystemAndMessages opened an "assistant"
// pending turn for the empty message, contributed zero content blocks to it
// (silently dropped by flush's len>0 guard), but the role-transition state
// still advanced - so the next user message started a NEW "user" turn
// instead of extending the one before the empty assistant message.
func TestAnthropicSkipsEmptyAssistantTurnWithoutBreakingAlternation(t *testing.T) {
	var gotBody map[string]any
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	})
	req := Request{
		Model: "claude-sonnet-5",
		Messages: []Message{
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, Content: "", ToolCalls: nil, ReasoningContent: ""}, // genuinely empty
			{Role: RoleUser, Content: "still there?"},
		},
	}
	if _, err := client.ChatTurn(context.Background(), req); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	msgs, _ := gotBody["messages"].([]any)
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		entry, _ := m.(map[string]any)
		roles[i], _ = entry["role"].(string)
	}
	for i := 1; i < len(roles); i++ {
		if roles[i] == roles[i-1] {
			t.Fatalf("messages roles = %v, want strict alternation (no two adjacent %q)", roles, roles[i])
		}
	}
}

// A tool_use block's ID must reach the wire unchanged so a later
// tool_result.tool_use_id exactly matches - Anthropic rejects a mismatch.
func TestAnthropicToolUseIDRoundTrips(t *testing.T) {
	var gotBody map[string]any
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	})
	req := Request{
		Model: "claude-sonnet-5",
		Messages: []Message{
			{Role: RoleUser, Content: "go"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{newToolCall("toolu_01ABC", "read_file", `{"path":"a.go"}`)}},
			{Role: RoleTool, ToolCallID: "toolu_01ABC", Content: "package a"},
		},
	}
	if _, err := client.ChatTurn(context.Background(), req); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	msgs, _ := gotBody["messages"].([]any)
	assistant, _ := msgs[1].(map[string]any)
	content, _ := assistant["content"].([]any)
	block, _ := content[0].(map[string]any)
	if block["id"] != "toolu_01ABC" {
		t.Fatalf("tool_use id = %v, want toolu_01ABC", block["id"])
	}
}

// A response with tool_use content produces a ToolCall whose Arguments is
// the JSON-encoded input object (the OpenAI shape's string convention), and
// FinishReason "tool_calls" for stop_reason "tool_use".
func TestAnthropicChatTurnToolUse(t *testing.T) {
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","id":"toolu_9","name":"read_file","input":{"path":"go.mod"}}],"stop_reason":"tool_use","usage":{}}`))
	})
	resp, err := client.ChatTurn(context.Background(), Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "read go.mod"}}})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "toolu_9" || resp.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("ToolCalls = %#v", resp.ToolCalls)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Function.Arguments), &args); err != nil || args["path"] != "go.mod" {
		t.Fatalf("Arguments = %q, want JSON {\"path\":\"go.mod\"}", resp.ToolCalls[0].Function.Arguments)
	}
}

// A thinking block round-trips as raw JSON in ReasoningContent (the
// signature-preservation strategy for the open question in the design doc),
// and replays byte-identical on the next turn's outbound translation.
func TestAnthropicThinkingBlockRoundTrips(t *testing.T) {
	var secondRequestBody map[string]any
	callCount := 0
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":"considering the file","signature":"sig-abc"},{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{}}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&secondRequestBody)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	})

	first, err := client.ChatTurn(context.Background(), Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("first ChatTurn: %v", err)
	}
	if first.ReasoningContent == "" || !strings.Contains(first.ReasoningContent, "sig-abc") {
		t.Fatalf("ReasoningContent = %q, want it to carry the raw thinking block including the signature", first.ReasoningContent)
	}

	// Replay: the assistant message from turn one, carrying the captured
	// ReasoningContent, goes back out on turn two exactly as history would
	// build it.
	req2 := Request{
		Model: "claude-sonnet-5",
		Messages: []Message{
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, Content: first.Content, ReasoningContent: first.ReasoningContent},
			{Role: RoleUser, Content: "continue"},
		},
	}
	if _, err := client.ChatTurn(context.Background(), req2); err != nil {
		t.Fatalf("second ChatTurn: %v", err)
	}
	msgs, _ := secondRequestBody["messages"].([]any)
	assistant, _ := msgs[1].(map[string]any)
	content, _ := assistant["content"].([]any)
	if len(content) == 0 {
		t.Fatal("replayed assistant message has no content blocks")
	}
	thinkingBlock, _ := content[0].(map[string]any)
	if thinkingBlock["type"] != "thinking" || thinkingBlock["signature"] != "sig-abc" {
		t.Fatalf("content[0] = %#v, want the replayed thinking block with its signature intact", thinkingBlock)
	}
}

// A pre-output refusal (HTTP 200, empty content array, stop_reason
// "refusal") must not be treated as a transport error: ChatTurn returns a
// Response with FinishReason FinishReasonRefusal and empty Content, not an
// error.
func TestAnthropicChatTurnRefusalEmptyContent(t *testing.T) {
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"refusal","usage":{}}`))
	})
	resp, err := client.ChatTurn(context.Background(), Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatTurn returned an error for a refusal, want a Response: %v", err)
	}
	if resp.Content != "" {
		t.Fatalf("Content = %q, want empty on a pre-output refusal", resp.Content)
	}
	if resp.FinishReason != FinishReasonRefusal {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, FinishReasonRefusal)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("ToolCalls = %#v, want none on a refusal", resp.ToolCalls)
	}
}

// A mid-stream refusal (partial content already billed) is not empty: the
// caller still sees whatever text arrived before the decline.
func TestAnthropicChatTurnRefusalPartialContent(t *testing.T) {
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Here's what I "}],"stop_reason":"refusal","usage":{}}`))
	})
	resp, err := client.ChatTurn(context.Background(), Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if resp.Content != "Here's what I " {
		t.Fatalf("Content = %q, want the partial pre-refusal text preserved", resp.Content)
	}
	if resp.FinishReason != FinishReasonRefusal {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, FinishReasonRefusal)
	}
}

// Non-2xx statuses become wrapped errors naming only the status and (when
// present) Anthropic's error type - never the message text, matching the
// privacy convention every other provider's error parser in this package
// follows.
func TestAnthropicNonOKStatusesBecomeErrors(t *testing.T) {
	cases := []struct {
		name         string
		statusCode   int
		body         string
		wantSubstr   []string
		wantNoSubstr string
	}{
		{"unauthorized", http.StatusUnauthorized, `{}`, []string{"auth failed", "HTTP 401"}, ""},
		{"bad request with type", http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"leak this request content: secret-token"}}`,
			[]string{"HTTP 400", "invalid_request_error"}, "leak this request content"},
		{"overloaded", http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error"}}`, []string{"HTTP 429", "rate_limit_error"}, ""},
		{"non-json body", http.StatusInternalServerError, `not json`, []string{"HTTP 500"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := client.ChatTurn(context.Background(), Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			for _, want := range tc.wantSubstr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err.Error(), want)
				}
			}
			if tc.wantNoSubstr != "" && strings.Contains(err.Error(), tc.wantNoSubstr) {
				t.Fatalf("error %q leaked provider message text %q", err.Error(), tc.wantNoSubstr)
			}
		})
	}
}

// The reasoning dialect + level resolved for a request reaches the wire as
// Anthropic's native thinking + output_config.effort shape (via
// reasoningBodyFields' DialectAnthropicAdaptive case), not a guessed one.
func TestAnthropicRequestCarriesResolvedReasoningFields(t *testing.T) {
	var gotBody map[string]any
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	})
	req := Request{
		Model:          "claude-sonnet-5",
		Messages:       []Message{{Role: RoleUser, Content: "hi"}},
		ReasoningLevel: reasoning.High,
	}
	if _, err := client.ChatTurn(context.Background(), req); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	thinking, _ := gotBody["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" {
		t.Fatalf("thinking = %#v, want type adaptive", gotBody["thinking"])
	}
	outputConfig, _ := gotBody["output_config"].(map[string]any)
	if outputConfig["effort"] != "high" {
		t.Fatalf("output_config = %#v, want effort high", gotBody["output_config"])
	}
}

// max_tokens is always sent (Anthropic requires it): a caller-set value
// wins, otherwise the effort-scaled floor applies.
func TestAnthropicMaxTokensDefaultsWhenUnset(t *testing.T) {
	var gotBody map[string]any
	client := newTestAnthropicClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	})
	req := Request{Model: "claude-sonnet-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}, ReasoningLevel: reasoning.XHigh}
	if _, err := client.ChatTurn(context.Background(), req); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	maxTokens, ok := gotBody["max_tokens"].(float64)
	if !ok || int(maxTokens) != 65536 {
		t.Fatalf("max_tokens = %v, want the xhigh floor 65536", gotBody["max_tokens"])
	}

	explicit := 222
	req.MaxTokens = &explicit
	if _, err := client.ChatTurn(context.Background(), req); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	maxTokens, ok = gotBody["max_tokens"].(float64)
	if !ok || int(maxTokens) != 222 {
		t.Fatalf("max_tokens = %v, want the caller-supplied 222", gotBody["max_tokens"])
	}
}

func newToolCall(id, name, arguments string) ToolCall {
	tc := ToolCall{ID: id, Type: "function"}
	tc.Function.Name = name
	tc.Function.Arguments = arguments
	return tc
}
