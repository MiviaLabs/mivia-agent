package provider

import "testing"

func TestNewOpenRouterAppliesDefaultsAndOverrides(t *testing.T) {
	comp, err := NewOpenRouter(Options{APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.httpReferer == "" || client.xTitle != "Mivia Agent" || client.baseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("client=%+v", client)
	}
	comp, err = NewOpenRouter(Options{APIKey: "fake", BaseURL: "https://example.com/v1", HTTPReferer: "https://ref.example", XTitle: "title"})
	if err != nil {
		t.Fatal(err)
	}
	client = comp.(*OpenAICompat)
	if client.baseURL != "https://example.com/v1" || client.httpReferer != "https://ref.example" || client.xTitle != "title" {
		t.Fatalf("client=%+v", client)
	}
}

// TestNewOpenRouterEnablesReasoningReplay pins OpenRouter's reasoning replay
// contract: assistant reasoning_content must be echoed back on subsequent
// tool-call turns using the wire field name "reasoning".
func TestNewOpenRouterEnablesReasoningReplay(t *testing.T) {
	comp, err := NewOpenRouter(Options{APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewOpenRouter must return *OpenAICompat, got %T", comp)
	}
	if !client.replayReasoning {
		t.Fatalf("NewOpenRouter must set replayReasoning, got %v", client.replayReasoning)
	}
	if client.replayReasoningField != "reasoning" {
		t.Fatalf("NewOpenRouter must set replayReasoningField=%q, got %q", "reasoning", client.replayReasoningField)
	}
}
