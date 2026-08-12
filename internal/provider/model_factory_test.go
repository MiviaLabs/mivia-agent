package provider

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestNewForProviderUsesQualifiedRuntimeRecord(t *testing.T) {
	res := &config.Resolved{ProviderName: "deepseek", Model: "deepseek/v4", ProviderRuntimes: map[string]config.ProviderRuntime{
		"deepseek": {ProviderName: "deepseek", BaseURL: "https://api.deepseek.com/v1", APIKeyEnv: "DEEPSEEK_API_KEY", APIKeySet: true, APIKey: "key"},
		"zai":      {ProviderName: "zai", BaseURL: "https://api.z.ai/api/paas/v4", APIKeyEnv: "ZAI_API_KEY", APIKeySet: true, APIKey: "key"},
	}}
	for _, name := range []string{"deepseek", "zai"} {
		comp, err := NewForProvider(res, name)
		if err != nil || comp == nil {
			t.Fatalf("provider %s: comp=%v err=%v", name, comp, err)
		}
	}
}

func TestNewForProviderFailsClosedWithoutCredential(t *testing.T) {
	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{
		"openrouter": {ProviderName: "openrouter", APIKeyEnv: "OPENROUTER_API_KEY"},
	}}
	_, err := NewForProvider(res, "openrouter")
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("error exposed credential environment detail: %q", err)
	}
}

// TestNewForProviderForwardsContextWindowTokensToOllama pins the wiring
// between the runtime model catalog and the ollama num_ctx override: the
// catalog lookup in NewForProvider must reach Options.ContextWindowTokens,
// and NewOllama must forward it as options.num_ctx in the request body.
// Without this link the DC-9 fix would silently no-op for real configs.
func TestNewForProviderForwardsContextWindowTokensToOllama(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "qwen3.6:27b-q4_K_M",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434/v1",
				Models: []config.ModelSpec{
					{Name: "qwen3.6:27b-q4_K_M", ContextWindowTokens: 32768},
				},
			},
		},
	}
	comp, err := NewForProvider(res, "ollama")
	if err != nil {
		t.Fatal(err)
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewForProvider must return *OpenAICompat, got %T", comp)
	}
	want := map[string]any{"options": map[string]any{"num_ctx": 32768}}
	if !reflect.DeepEqual(client.extraBody, want) {
		t.Fatalf("extraBody=%#v, want %#v", client.extraBody, want)
	}
}

// TestNewForProviderOmitsNumCtxWhenModelUncataloged pins the no-op path: a
// resolved model absent from the catalog yields ContextWindowTokens 0, so no
// num_ctx override reaches the request body.
func TestNewForProviderOmitsNumCtxWhenModelUncataloged(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "unlisted:latest",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434/v1",
				Models: []config.ModelSpec{
					{Name: "qwen3.6:27b-q4_K_M", ContextWindowTokens: 32768},
				},
			},
		},
	}
	comp, err := NewForProvider(res, "ollama")
	if err != nil {
		t.Fatal(err)
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewForProvider must return *OpenAICompat, got %T", comp)
	}
	if len(client.extraBody) != 0 {
		t.Fatalf("extraBody=%#v, want empty", client.extraBody)
	}
}

// TestNewForProviderFallbackModelProfilesForwardsNumCtx pins the fallback
// path: when ProviderRuntimes has no record for the active provider, runtime
// models come from res.ModelProfiles, and the same num_ctx wiring must hold.
func TestNewForProviderFallbackModelProfilesForwardsNumCtx(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "qwen3.6:27b-q4_K_M",
		BaseURL:      "http://127.0.0.1:11434/v1",
		ModelProfiles: []config.ModelSpec{
			{Name: "qwen3.6:27b-q4_K_M", ContextWindowTokens: 32768},
		},
	}
	comp, err := NewForProvider(res, "ollama")
	if err != nil {
		t.Fatal(err)
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewForProvider must return *OpenAICompat, got %T", comp)
	}
	want := map[string]any{"options": map[string]any{"num_ctx": 32768}}
	if !reflect.DeepEqual(client.extraBody, want) {
		t.Fatalf("extraBody=%#v, want %#v", client.extraBody, want)
	}
}
