package provider

import (
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// The openrouter factory must honor Options.CacheMarkersEnabled: the built
// client marks the stable prefix with explicit cache_control content parts.
func TestNewOpenRouterEnablesCacheMarkersFromOptions(t *testing.T) {
	completer, err := NewOpenRouter(Options{APIKey: "k", CacheMarkersEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := completer.(*OpenAICompat)
	if !ok {
		t.Fatalf("completer type = %T, want *OpenAICompat", completer)
	}
	if !client.cacheMarkersEnabled {
		t.Fatal("cacheMarkersEnabled = false, want true")
	}
	raw, err := client.marshalBody(Request{Model: "m", Messages: []Message{
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
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want 2 entries", body["messages"])
	}
	assertCacheMarkedContent(t, messages[0], RoleSystem, "sys")
	assertCacheMarkedContent(t, messages[1], RoleUser, "hi")
}

// Options.CacheMarkersEnabled false keeps openrouter bodies plain strings.
func TestNewOpenRouterMarkersOffKeepsPlainContent(t *testing.T) {
	completer, err := NewOpenRouter(Options{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	client := completer.(*OpenAICompat)
	if client.cacheMarkersEnabled {
		t.Fatal("cacheMarkersEnabled = true, want false")
	}
}

// Implicit-cache factories never emit markers even when the resolved option
// requests them, so their request bodies stay byte-identical.
func TestImplicitCacheFactoriesIgnoreCacheMarkersOption(t *testing.T) {
	factories := map[string]func(Options) (Completer, error){
		"deepseek": NewDeepSeek,
		"zai":      NewZAI,
	}
	for name, factory := range factories {
		completer, err := factory(Options{APIKey: "k", CacheMarkersEnabled: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if completer.(*OpenAICompat).cacheMarkersEnabled {
			t.Fatalf("%s: cacheMarkersEnabled = true, want false", name)
		}
	}
}

// NewForProvider wires prompt_cache into explicit markers: the default
// ("auto") enables them on openrouter, and "off" disables them.
func TestNewForProviderPromptCacheGatesMarkers(t *testing.T) {
	for _, tc := range []struct {
		promptCache string
		want        bool
	}{{"auto", true}, {"off", false}} {
		resolved := &config.Resolved{
			ProviderName: "openrouter", APIKey: "k", APIKeySet: true,
			Model: "m", PromptCache: tc.promptCache,
		}
		completer, err := NewForProvider(resolved, "openrouter")
		if err != nil {
			t.Fatalf("prompt_cache=%s: %v", tc.promptCache, err)
		}
		if got := completer.(*OpenAICompat).cacheMarkersEnabled; got != tc.want {
			t.Fatalf("prompt_cache=%s: cacheMarkersEnabled = %v, want %v", tc.promptCache, got, tc.want)
		}
	}
}
