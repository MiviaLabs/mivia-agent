package provider

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func TestNewLLMProxyCLIAppliesDefaultsAndOverrides(t *testing.T) {
	comp, err := NewLLMProxyCLI(Options{APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.baseURL != "http://127.0.0.1:8317/v1" {
		t.Fatalf("baseURL=%q, want default http://127.0.0.1:8317/v1", client.baseURL)
	}
	if client.reasoning != reasoning.DialectOpenAI {
		t.Fatalf("reasoning=%q, want %q", client.reasoning, reasoning.DialectOpenAI)
	}
	if !client.replayReasoning {
		t.Fatalf("replayReasoning=%v, want true", client.replayReasoning)
	}
	if client.sendSessionUserKey {
		t.Fatalf("sendSessionUserKey=%v, want false for local proxy", client.sendSessionUserKey)
	}

	comp, err = NewLLMProxyCLI(Options{APIKey: "fake", BaseURL: "http://127.0.0.1:9000/v1"})
	if err != nil {
		t.Fatal(err)
	}
	client = comp.(*OpenAICompat)
	if client.baseURL != "http://127.0.0.1:9000/v1" {
		t.Fatalf("baseURL=%q, want override", client.baseURL)
	}
}

func TestNewForProviderLLMProxyCLISucceeds(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "llmproxycli",
		BaseURL:      "http://127.0.0.1:8317/v1",
		APIKeyEnv:    "CLIPROXY_API_KEY",
		APIKey:       "fake-key",
		APIKeySet:    true,
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"llmproxycli": {
				ProviderName: "llmproxycli",
				BaseURL:      "http://127.0.0.1:8317/v1",
				APIKeyEnv:    "CLIPROXY_API_KEY",
				APIKey:       "fake-key",
				APIKeySet:    true,
			},
		},
	}
	comp, err := NewForProvider(res, "llmproxycli")
	if err != nil || comp.Name() != "llmproxycli" {
		t.Fatalf("comp=%T err=%v", comp, err)
	}
}
