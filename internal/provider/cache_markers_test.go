package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestCacheMarkersGatedOnEmitContentParts locks in the wire shape for a client
// with CacheMarkersEnabled: the stable prefix (system message and the FIRST
// user message) is emitted as Anthropic-style content parts carrying a
// cache_control:{type:"ephemeral"} marker, while tool results keep plain
// string content and assistant turns are untouched.
func TestCacheMarkersGatedOnEmitContentParts(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: "https://example.test", APIKey: "k",
		CacheMarkersEnabled: true,
	})
	req := Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleSystem, Content: "you are a test assistant"},
			{Role: RoleUser, Content: "hello"},
			{Role: RoleAssistant, Content: "I will read the file", ToolCalls: []ToolCall{toolCallFor("call_1", "read_file", `{"path":"a.txt"}`)}},
			{Role: RoleTool, ToolCallID: "call_1", Content: "tool result"},
		},
	}
	raw, err := c.marshalBody(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding marshaled body: %v\nbody: %s", err, raw)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("messages = %#v, want 4 entries", body["messages"])
	}
	// System and first user messages are the stable prefix: they must become
	// content parts carrying the ephemeral cache marker.
	assertCacheMarkedContent(t, messages[0], RoleSystem, "you are a test assistant")
	assertCacheMarkedContent(t, messages[1], RoleUser, "hello")
	// Assistant turns keep the plain string shape; the newest tool result
	// carries the rolling breakpoint so the accumulated transcript caches.
	assertPlainStringContent(t, messages[2], RoleAssistant, "I will read the file")
	assertCacheMarkedContent(t, messages[3], RoleTool, "tool result")
}

// The rolling breakpoint tracks the NEWEST user or tool message: older tool
// results lose the marker as it moves forward, and a trailing assistant turn
// is walked past rather than marked.
func TestRollingBreakpointMarksNewestUserOrToolMessage(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: "https://example.test", APIKey: "k",
		CacheMarkersEnabled: true,
	})
	raw, err := c.marshalBody(Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "objective"},
			{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{toolCallFor("call_1", "read_file", `{"path":"a"}`)}},
			{Role: RoleTool, ToolCallID: "call_1", Content: "older result"},
			{Role: RoleUser, Content: "steer"},
			{Role: RoleAssistant, Content: "done"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	assertCacheMarkedContent(t, messages[0], RoleSystem, "sys")
	assertCacheMarkedContent(t, messages[1], RoleUser, "objective")
	// Older tool result stays plain: the rolling marker has moved past it.
	assertPlainStringContent(t, messages[3], RoleTool, "older result")
	// Newest user message carries the rolling marker; the trailing assistant
	// turn is walked past, never marked.
	assertCacheMarkedContent(t, messages[4], RoleUser, "steer")
	assertPlainStringContent(t, messages[5], RoleAssistant, "done")
}

// A single-turn request (system + one user message) places the fixed
// first-user marker only once - the rolling pass must not double-wrap it.
func TestRollingBreakpointSkipsFirstUserMessage(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: "https://example.test", APIKey: "k",
		CacheMarkersEnabled: true,
	})
	raw, err := c.marshalBody(Request{Model: "m", Messages: []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hi"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	assertCacheMarkedContent(t, messages[1], RoleUser, "hi")
}

// assertCacheMarkedContent checks one decoded message whose content is exactly
// one text part with a cache_control:{type:"ephemeral"} marker.
func assertCacheMarkedContent(t *testing.T, entry any, role, text string) {
	t.Helper()
	msg, ok := entry.(map[string]any)
	if !ok {
		t.Fatalf("message entry = %#v, want a JSON object", entry)
	}
	if msg["role"] != role {
		t.Fatalf("role = %#v, want %q", msg["role"], role)
	}
	parts, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("content = %#v, want a content-part array", msg["content"])
	}
	if len(parts) != 1 {
		t.Fatalf("content parts = %#v, want exactly 1", parts)
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("content part = %#v, want a JSON object", parts[0])
	}
	if part["type"] != "text" {
		t.Fatalf("part type = %#v, want %q", part["type"], "text")
	}
	if part["text"] != text {
		t.Fatalf("part text = %#v, want %q", part["text"], text)
	}
	cc, ok := part["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("cache_control = %#v, want an object", part["cache_control"])
	}
	if cc["type"] != "ephemeral" {
		t.Fatalf("cache_control.type = %#v, want %q", cc["type"], "ephemeral")
	}
}

// assertPlainStringContent checks one decoded message whose content is a plain
// JSON string (the shape markers must not touch).
func assertPlainStringContent(t *testing.T, entry any, role, text string) {
	t.Helper()
	msg, ok := entry.(map[string]any)
	if !ok {
		t.Fatalf("message entry = %#v, want a JSON object", entry)
	}
	if msg["role"] != role {
		t.Fatalf("role = %#v, want %q", msg["role"], role)
	}
	content, ok := msg["content"].(string)
	if !ok {
		t.Fatalf("content = %#v, want a plain string", msg["content"])
	}
	if content != text {
		t.Fatalf("content = %q, want %q", content, text)
	}
}

// TestCacheMarkersGatedOffKeepByteIdenticalBody pins that markers-off is a
// no-op: the body is byte-identical to the shape every request sends today
// (plain string content, no cache_control anywhere). This passes today and
// must keep passing once marker emission lands.
func TestCacheMarkersGatedOffKeepByteIdenticalBody(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "you are a test assistant"},
		{Role: RoleUser, Content: "hello"},
	}
	req := Request{Model: "m", Messages: msgs}

	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: "https://example.test", APIKey: "k",
		CacheMarkersEnabled: false,
	})
	raw, err := c.marshalBody(req)
	if err != nil {
		t.Fatal(err)
	}
	// Byte-for-byte pin: marshalBody round-trips through a map, so key order
	// is deterministic (sorted), making this literal the exact body a
	// pre-marker client produces today.
	want := `{"messages":[{"content":"you are a test assistant","role":"system"},{"content":"hello","role":"user"}],"model":"m","stream":false}`
	if string(raw) != want {
		t.Fatalf("markers-off body changed:\n got: %s\nwant: %s", raw, want)
	}
	if strings.Contains(string(raw), "cache_control") {
		t.Fatalf("markers-off body must not contain cache_control anywhere: %s", raw)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding marshaled body: %v", err)
	}
	for _, entry := range body["messages"].([]any) {
		msg := entry.(map[string]any)
		if _, isString := msg["content"].(string); !isString {
			t.Fatalf("markers-off content must stay a plain string, got %#v", msg["content"])
		}
	}
}

// TestCheckReservedExtrasBlocksCacheKeys locks in that cache-marker wire keys
// are operator-reserved: an extraBody naming cache_control (or a
// prompt_cache_* alias some OpenAI-compatible dialects honor) must be refused,
// or a caller could smuggle a conflicting marker onto the wire.
// checkReservedExtras now refuses them alongside model/messages/stream.
func TestCheckReservedExtrasBlocksCacheKeys(t *testing.T) {
	for _, key := range []string{"cache_control", "prompt_cache_breakpoint", "prompt_cache_options"} {
		t.Run(key, func(t *testing.T) {
			c := NewOpenAICompatWithOptions(CompatOptions{
				Name: "test", BaseURL: "https://example.test", APIKey: "k",
				ExtraBody: map[string]any{key: "anything"},
			})
			_, err := c.newRequest(context.Background(), Request{
				Model:    "m",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			if err == nil {
				t.Fatalf("extraBody %q must be refused as reserved, got no error", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error = %v, want it to name the reserved key %q", err, key)
			}
		})
	}
}

// TestCacheUsageReportsExplicitStyleWhenMarkersEnabled locks in the reported
// CacheUsage.Style: a client that sends explicit cache markers reports
// CacheStyleExplicit, a client that does not reports CacheStyleImplicit.
func TestCacheUsageReportsExplicitStyleWhenMarkersEnabled(t *testing.T) {
	hit := 42
	usage := &usageWire{PromptTokens: 100, PromptCacheHitTokens: &hit}

	withMarkers := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: "https://example.test", APIKey: "k",
		CacheUsageEnabled: true, CacheMarkersEnabled: true,
	})
	got := withMarkers.cacheUsage(usage)
	if !got.Reported || got.Style != CacheStyleExplicit {
		t.Fatalf("markers-enabled usage = %+v, want reported with Style %q", got, CacheStyleExplicit)
	}

	withoutMarkers := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: "https://example.test", APIKey: "k",
		CacheUsageEnabled: true,
	})
	got = withoutMarkers.cacheUsage(usage)
	if !got.Reported || got.Style != CacheStyleImplicit {
		t.Fatalf("markers-off usage = %+v, want reported with Style %q", got, CacheStyleImplicit)
	}
}
