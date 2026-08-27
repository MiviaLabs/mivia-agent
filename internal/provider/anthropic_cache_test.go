package provider

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestAnthropicPromptTokensCountCachedInput pins the accounting rule that
// keeps prompt caching from silently disabling auto-compaction.
//
// Anthropic's input_tokens counts only the UNCACHED remainder; the cached and
// newly-written prompt tokens are reported in their own fields. Reading
// input_tokens alone reports a well-cached step as a tiny prompt, and
// internal/agent's Loop.Calibration divides that by the host's own estimate
// and drags the correction ratio toward its 0.2 floor - scaling every future
// compaction estimate down with it until the trigger stops firing.
func TestAnthropicPromptTokensCountCachedInput(t *testing.T) {
	usage := anthropicUsage{
		InputTokens:              120,
		OutputTokens:             40,
		CacheReadInputTokens:     8000,
		CacheCreationInputTokens: 300,
	}
	if got, want := usage.promptTokens(), 8420; got != want {
		t.Errorf("promptTokens() = %d, want %d (uncached + cache read + cache write)", got, want)
	}
}

// TestAnthropicUncachedResponseReportsNoCacheUsage keeps a deployment with no
// caching quiet: CacheUsage.Reported false means "not reported", and
// EmitCacheUsage skips it, rather than announcing "0% cached" every turn.
func TestAnthropicUncachedResponseReportsNoCacheUsage(t *testing.T) {
	usage := anthropicUsage{InputTokens: 500, OutputTokens: 40}
	if got := usage.cacheUsage(); got.Reported {
		t.Errorf("cacheUsage() reported on a response carrying no cache fields: %+v", got)
	}
	if got, want := usage.promptTokens(), 500; got != want {
		t.Errorf("promptTokens() with caching off = %d, want %d", got, want)
	}
}

func TestAnthropicCacheUsageReportsExplicitStyle(t *testing.T) {
	usage := anthropicUsage{InputTokens: 120, CacheReadInputTokens: 8000, CacheCreationInputTokens: 300}
	got := usage.cacheUsage()
	want := CacheUsage{
		Reported:          true,
		Style:             CacheStyleExplicit,
		InputTokens:       8420,
		CachedInputTokens: 8000,
		CacheWriteTokens:  300,
	}
	if got != want {
		t.Errorf("cacheUsage() = %+v, want %+v", got, want)
	}
}

// TestAnthropicResponseCarriesCacheAccounting pins the two derivations at the
// non-stream decode boundary, where the agent loop actually reads them.
func TestAnthropicResponseCarriesCacheAccounting(t *testing.T) {
	var wire anthropicResponse
	body := `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":120,"output_tokens":40,
		"cache_read_input_tokens":8000,"cache_creation_input_tokens":300}}`
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatal(err)
	}
	resp := anthropicResponseToProvider(wire)
	if got, want := resp.TokenUsage.InputTokens, 8420; got != want {
		t.Errorf("TokenUsage.InputTokens = %d, want %d", got, want)
	}
	if !resp.CacheUsage.Reported || resp.CacheUsage.CachedInputTokens != 8000 {
		t.Errorf("CacheUsage = %+v, want a report of 8000 cached tokens", resp.CacheUsage)
	}
}

// requestMessages decodes the wire messages a built body carries, so the
// marker assertions read the same shape the provider receives.
func requestMessages(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Messages
}

func blockHasCacheControl(block any) bool {
	m, ok := block.(map[string]any)
	if !ok {
		return false
	}
	_, ok = m["cache_control"]
	return ok
}

func cacheControlCount(t *testing.T, body map[string]any) int {
	t.Helper()
	count := 0
	if system, ok := body["system"].([]any); ok {
		for _, block := range system {
			if blockHasCacheControl(block) {
				count++
			}
		}
	}
	for _, msg := range requestMessages(t, body) {
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			if blockHasCacheControl(block) {
				count++
			}
		}
	}
	return count
}

func anthropicTestToolCall() ToolCall {
	tc := ToolCall{ID: "call-1", Type: "function"}
	tc.Function.Name = "read_file"
	tc.Function.Arguments = `{"path":"a"}`
	return tc
}

func anthropicTestRequest(msgs []Message) Request {
	return Request{Model: "claude-sonnet-5", Messages: msgs}
}

// TestAnthropicCacheMarkersAreOffByDefault keeps [provider] prompt_cache =
// "off" honest: the request body must stay byte-identical to the pre-marker
// layout, with system a plain string and no cache_control anywhere.
func TestAnthropicCacheMarkersAreOffByDefault(t *testing.T) {
	c := newAnthropicCompleter("anthropic", "https://example.invalid", "key", nil, false)
	body, err := c.buildRequestBody(anthropicTestRequest([]Message{
		{Role: RoleSystem, Content: "system prompt"},
		{Role: RoleUser, Content: "hello"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["system"].(string); !ok {
		t.Errorf("system = %T, want an unmodified plain string when markers are off", body["system"])
	}
	if got := cacheControlCount(t, body); got != 0 {
		t.Errorf("cache_control markers with the option off = %d, want 0", got)
	}
}

// TestAnthropicCacheMarkersMarkTheStablePrefix pins the placement policy:
// system, the first user message, and a rolling marker on the newest stable
// user turn - three of Anthropic's budget of four.
func TestAnthropicCacheMarkersMarkTheStablePrefix(t *testing.T) {
	c := newAnthropicCompleter("anthropic", "https://example.invalid", "key", nil, true)
	body, err := c.buildRequestBody(anthropicTestRequest([]Message{
		{Role: RoleSystem, Content: "system prompt"},
		{Role: RoleUser, Content: "first objective"},
		{Role: RoleAssistant, Content: "thinking", ToolCalls: []ToolCall{anthropicTestToolCall()}},
		{Role: RoleTool, ToolCallID: "call-1", Content: "file body"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	system, ok := body["system"].([]any)
	if !ok || len(system) != 1 || !blockHasCacheControl(system[0]) {
		t.Fatalf("system = %#v, want a single marked text block", body["system"])
	}
	if got := cacheControlCount(t, body); got != 3 {
		t.Errorf("cache_control markers = %d, want 3 (system, first user, rolling)", got)
	}
	messages := requestMessages(t, body)
	last := messages[len(messages)-1]
	if last["role"] != "user" {
		t.Fatalf("last wire message role = %v, want the coalesced tool-result user turn", last["role"])
	}
	blocks := last["content"].([]any)
	if !blockHasCacheControl(blocks[len(blocks)-1]) {
		t.Errorf("rolling breakpoint missing from the newest stable user turn: %#v", blocks)
	}
}

// TestAnthropicRollingMarkerSkipsEphemeralHostText keeps the rolling
// breakpoint on content that will still be there next step. A NAMED user
// message is a host injection (the context summary, a conclude nudge) that
// never recurs, so anchoring on it guarantees a cache miss on the very next
// request - the marker must fall on the tool result ahead of it instead.
func TestAnthropicRollingMarkerSkipsEphemeralHostText(t *testing.T) {
	c := newAnthropicCompleter("anthropic", "https://example.invalid", "key", nil, true)
	body, err := c.buildRequestBody(anthropicTestRequest([]Message{
		{Role: RoleSystem, Content: "system prompt"},
		{Role: RoleUser, Content: "first objective"},
		{Role: RoleAssistant, Content: "thinking", ToolCalls: []ToolCall{anthropicTestToolCall()}},
		{Role: RoleTool, ToolCallID: "call-1", Content: "file body"},
		{Role: RoleUser, Name: "context_summary", Content: "ephemeral host summary"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	messages := requestMessages(t, body)
	blocks := messages[len(messages)-1]["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("coalesced turn blocks = %d, want the tool result plus the host text", len(blocks))
	}
	if blockHasCacheControl(blocks[1]) {
		t.Errorf("rolling breakpoint anchored on ephemeral host text: %#v", blocks[1])
	}
	if !blockHasCacheControl(blocks[0]) {
		t.Errorf("rolling breakpoint did not fall back to the stable tool result: %#v", blocks[0])
	}
}

// TestAnthropicStreamCarriesCacheAccounting pins the same accounting on the
// STREAMING path, which assembles usage across two events: message_start
// carries every prompt-side field (including the cache counters) and
// message_delta carries the final output_tokens. Both must survive into the
// Response, or a streaming session - the shape the agent loop actually runs -
// keeps the calibration-poisoning undercount the non-stream path no longer
// has.
func TestAnthropicStreamCarriesCacheAccounting(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("message_start", map[string]any{"message": map[string]any{"usage": map[string]any{
		"input_tokens":                120,
		"cache_read_input_tokens":     8000,
		"cache_creation_input_tokens": 300,
	}}}))
	b.WriteString(sseEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}}))
	b.WriteString(sseEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "hi"}}))
	b.WriteString(sseEvent("content_block_stop", map[string]any{"index": 0}))
	b.WriteString(sseEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 40}}))
	b.WriteString(sseEvent("message_stop", map[string]any{}))

	resp, err := decodeAnthropicStream(strings.NewReader(b.String()), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resp.TokenUsage.InputTokens, 8420; got != want {
		t.Errorf("TokenUsage.InputTokens = %d, want %d (message_start's cache counters must survive)", got, want)
	}
	if got, want := resp.TokenUsage.OutputTokens, 40; got != want {
		t.Errorf("TokenUsage.OutputTokens = %d, want %d (message_delta must not be lost to message_start)", got, want)
	}
	if !resp.CacheUsage.Reported || resp.CacheUsage.CachedInputTokens != 8000 || resp.CacheUsage.CacheWriteTokens != 300 {
		t.Errorf("CacheUsage = %+v, want 8000 cached / 300 written", resp.CacheUsage)
	}
}
