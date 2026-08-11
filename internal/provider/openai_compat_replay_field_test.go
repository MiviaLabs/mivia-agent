package provider

import (
	"encoding/json"
	"testing"
)

// marshalBodyAssistantMessage runs marshalBody for a single-turn request and
// returns the decoded assistant message, so tests can assert on the exact
// reasoning wire keys without depending on map iteration order.
func marshalBodyAssistantMessage(t *testing.T, c *OpenAICompat, msgs []Message) map[string]any {
	t.Helper()
	raw, err := c.marshalBody(Request{Model: "m", Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode marshalled body: %v (%s)", err, raw)
	}
	for _, m := range body.Messages {
		if m["role"] == RoleAssistant {
			return m
		}
	}
	t.Fatalf("no assistant message in marshalled body: %s", raw)
	return nil
}

// TestMarshalBodyRenamesReasoningContentToReasoningForOpenRouter pins the
// OpenRouter dialect: assistant reasoning is replayed under the provider's
// "reasoning" field, and the legacy "reasoning_content" key must not appear.
func TestMarshalBodyRenamesReasoningContentToReasoningForOpenRouter(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name:                    "openrouter",
		BaseURL:                 "https://example.invalid/v1",
		APIKey:                  "k",
		RequiresReasoningReplay: true,
		ReplayReasoningField:    "reasoning",
	})
	msgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "yo", ReasoningContent: "thoughts"},
	}
	assistant := marshalBodyAssistantMessage(t, c, msgs)
	if _, present := assistant["reasoning_content"]; present {
		t.Fatalf("assistant message must not carry reasoning_content when ReplayReasoningField is set: %#v", assistant)
	}
	if got, _ := assistant["reasoning"].(string); got != "thoughts" {
		t.Fatalf("assistant message must replay reasoning under %q, got %#v", "reasoning", assistant)
	}
}

// TestMarshalBodyKeepsReasoningContentForDeepSeek pins the DeepSeek dialect:
// an empty ReplayReasoningField means the legacy "reasoning_content" key stays
// and no "reasoning" key is invented.
func TestMarshalBodyKeepsReasoningContentForDeepSeek(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name:                    "deepseek",
		BaseURL:                 "https://example.invalid/v1",
		APIKey:                  "k",
		RequiresReasoningReplay: true,
	})
	msgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "yo", ReasoningContent: "thoughts"},
	}
	assistant := marshalBodyAssistantMessage(t, c, msgs)
	if got, _ := assistant["reasoning_content"].(string); got != "thoughts" {
		t.Fatalf("deepseek must keep reasoning_content, got %#v", assistant)
	}
	if _, present := assistant["reasoning"]; present {
		t.Fatalf("deepseek must not emit a reasoning key when ReplayReasoningField is empty: %#v", assistant)
	}
}

// TestMarshalBodyReplayDisabledHasNoReasoningKey is the byte-stability pin:
// with replay disabled neither reasoning key may reach the wire, so
// non-adopting request bodies stay byte-identical to pre-reasoning ones.
func TestMarshalBodyReplayDisabledHasNoReasoningKey(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name:    "plain",
		BaseURL: "https://example.invalid/v1",
		APIKey:  "k",
	})
	msgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "yo", ReasoningContent: "thoughts"},
	}
	assistant := marshalBodyAssistantMessage(t, c, msgs)
	if _, present := assistant["reasoning"]; present {
		t.Fatalf("replay off must not emit reasoning key: %#v", assistant)
	}
	if _, present := assistant["reasoning_content"]; present {
		t.Fatalf("replay off must not emit reasoning_content key: %#v", assistant)
	}
}
