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

func TestNewForProviderOllamaNonLoopbackHTTPNeverRelaxes(t *testing.T) {
	// The keyless relaxation is scoped to loopback only: a plain http URL on a
	// non-loopback host must still fail closed at the NewForProvider switch path.
	res := &config.Resolved{ProviderName: "ollama", BaseURL: "http://ollama.example.test/v1", APIKeyEnv: "OLLAMA_API_KEY", APIKeySet: false}
	_, err := NewForProvider(res, "ollama")
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("err=%v, want 'missing API key'", err)
	}

	res.APIKeySet = true
	res.APIKey = "fake"
	comp, err := NewForProvider(res, "ollama")
	if err != nil || comp == nil {
		t.Fatalf("comp=%T err=%v", comp, err)
	}
	if comp.Name() != "ollama" {
		t.Fatalf("name=%q, want %q", comp.Name(), "ollama")
	}
}

func TestNewDispatchesZAI(t *testing.T) {
	res := &config.Resolved{ProviderName: "zai", BaseURL: "https://api.z.ai/api/paas/v4", APIKey: "fake", APIKeySet: true}
	comp, err := New(res)
	if err != nil || comp.Name() != "zai" {
		t.Fatalf("comp=%T err=%v", comp, err)
	}
}

// TestContextWindowTokensFor pins the catalog lookup semantics used by
// NewForProvider to resolve the model's declared context capacity: exact
// match, first match wins, and 0 when the name is absent.
func TestContextWindowTokensFor(t *testing.T) {
	catalog := []config.ModelSpec{
		{Name: "qwen3.6:27b-q4_K_M", ContextWindowTokens: 32768},
		{Name: "gpt-oss:20b", ContextWindowTokens: 131072},
	}
	tests := []struct {
		name   string
		models []config.ModelSpec
		lookup string
		want   int
	}{
		{name: "exact match returns declared window", models: catalog, lookup: "qwen3.6:27b-q4_K_M", want: 32768},
		{name: "later entry matches too", models: catalog, lookup: "gpt-oss:20b", want: 131072},
		{name: "absent name returns zero", models: catalog, lookup: "unlisted:latest", want: 0},
		{name: "empty catalog returns zero", models: nil, lookup: "qwen3.6:27b-q4_K_M", want: 0},
		{
			name:   "first match wins on duplicates",
			models: []config.ModelSpec{{Name: "dup", ContextWindowTokens: 4096}, {Name: "dup", ContextWindowTokens: 8192}},
			lookup: "dup",
			want:   4096,
		},
		{
			name:   "no normalization - whitespace differs",
			models: []config.ModelSpec{{Name: " llama3", ContextWindowTokens: 4096}},
			lookup: "llama3",
			want:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contextWindowTokensFor(tt.models, tt.lookup); got != tt.want {
				t.Fatalf("contextWindowTokensFor(%q) = %d, want %d", tt.lookup, got, tt.want)
			}
		})
	}
}
