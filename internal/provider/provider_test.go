package provider

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestFactoryRegistryRejectsDuplicateAndKeepsSortedNames(t *testing.T) {
	r := newFactoryRegistry()
	factory := func(Options) (Completer, error) { return nil, nil }
	if err := r.register("openrouter", factory); err != nil {
		t.Fatal(err)
	}
	if err := r.register("deepseek", factory); err != nil {
		t.Fatal(err)
	}
	if err := r.register("DeepSeek", factory); err == nil {
		t.Fatal("expected duplicate error")
	}
	if got := strings.Join(r.names(), ","); got != "deepseek,openrouter" {
		t.Fatalf("names=%q", got)
	}
}

func TestNewDispatchesBuiltinsAndRejectsUnknown(t *testing.T) {
	res := &config.Resolved{ProviderName: "deepseek", BaseURL: "https://example.com/v1", APIKey: "fake", APIKeySet: true}
	comp, err := New(res)
	if err != nil || comp.Name() != "deepseek" {
		t.Fatalf("comp=%T err=%v", comp, err)
	}
	res.ProviderName = "unknown"
	_, err = New(res)
	if err == nil || !strings.Contains(err.Error(), "available: deepseek, ollama, openrouter, zai") {
		t.Fatalf("err=%v", err)
	}
}

func TestNewForProviderOllamaCloudFailsClosedWithoutKey(t *testing.T) {
	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{"ollama": {ProviderName: "ollama", BaseURL: "https://ollama.com/v1", APIKeyEnv: "OLLAMA_API_KEY"}}}
	_, err := NewForProvider(res, "ollama")
	if err == nil || !strings.Contains(err.Error(), "missing API key") || strings.Contains(err.Error(), "OLLAMA_API_KEY") {
		t.Fatalf("err=%v", err)
	}
}

func TestNewForProviderOllamaCloudSucceedsWithKey(t *testing.T) {
	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{"ollama": {ProviderName: "ollama", BaseURL: "https://ollama.com/v1", APIKeyEnv: "OLLAMA_API_KEY", APIKeySet: true, APIKey: "fake"}}}
	comp, err := NewForProvider(res, "ollama")
	if err != nil || comp.Name() != "ollama" {
		t.Fatalf("comp=%T err=%v", comp, err)
	}
}

func TestNewForProviderOllamaLoopbackSucceedsWithoutKey(t *testing.T) {
	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{"ollama": {ProviderName: "ollama", BaseURL: "http://127.0.0.1:11434/v1", APIKeyEnv: "OLLAMA_API_KEY", APIKeySet: false}}}
	comp, err := NewForProvider(res, "ollama")
	if err != nil || comp == nil {
		t.Fatalf("comp=%T err=%v", comp, err)
	}
	if comp.Name() != "ollama" {
		t.Fatalf("name=%q, want %q", comp.Name(), "ollama")
	}
	oc, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("comp type=%T, want *OpenAICompat", comp)
	}
	if oc.apiKey != "" {
		t.Fatalf("apiKey=%q, want empty (no stray key on loopback client)", oc.apiKey)
	}
}

func TestNewForProviderOllamaCloudStillFailsClosedWithoutKey(t *testing.T) {
	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{"ollama": {ProviderName: "ollama", BaseURL: "https://ollama.com/v1", APIKeyEnv: "OLLAMA_API_KEY", APIKeySet: false}}}
	_, err := NewForProvider(res, "ollama")
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("err=%v", err)
	}
}

func TestNewForProviderNonOllamaLoopbackStillFailsClosed(t *testing.T) {
	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{"deepseek": {ProviderName: "deepseek", BaseURL: "http://127.0.0.1:9999", APIKeyEnv: "DEEPSEEK_API_KEY", APIKeySet: false}}}
	_, err := NewForProvider(res, "deepseek")
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("err=%v", err)
	}
}

func TestNewDispatchesZAI(t *testing.T) {
	res := &config.Resolved{ProviderName: "zai", BaseURL: "https://api.z.ai/api/paas/v4", APIKey: "fake", APIKeySet: true}
	comp, err := New(res)
	if err != nil || comp.Name() != "zai" {
		t.Fatalf("comp=%T err=%v", comp, err)
	}
}
