package provider

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func TestNewLLMGatewayAppliesDefaultsAndOverrides(t *testing.T) {
	comp, err := NewLLMGateway(Options{APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.baseURL != "https://api.llmgateway.io/v1" {
		t.Fatalf("baseURL=%q", client.baseURL)
	}
	if !client.sendSessionUserKey {
		t.Fatalf("sendSessionUserKey=%v, want true", client.sendSessionUserKey)
	}
	if client.reasoning != reasoning.DialectOpenAI {
		t.Fatalf("reasoning=%q, want %q", client.reasoning, reasoning.DialectOpenAI)
	}
	comp, err = NewLLMGateway(Options{APIKey: "fake", BaseURL: "https://example.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	client = comp.(*OpenAICompat)
	if client.baseURL != "https://example.com/v1" {
		t.Fatalf("baseURL=%q, want the override", client.baseURL)
	}
}

// TestNewLLMGatewayIgnoresCacheMarkersOption pins the deliberate
// gateway-owns-markers decision: the gateway injects its own cache_control
// markers with per-model minimums and TTL ordering mivia cannot know, so the
// factory never forwards CacheMarkersEnabled even when the caller sets it.
func TestNewLLMGatewayIgnoresCacheMarkersOption(t *testing.T) {
	comp, err := NewLLMGateway(Options{APIKey: "fake", CacheMarkersEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.cacheMarkersEnabled {
		t.Fatalf("cacheMarkersEnabled=%v, want false regardless of the option", client.cacheMarkersEnabled)
	}
}
