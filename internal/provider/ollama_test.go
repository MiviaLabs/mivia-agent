package provider

import (
	"reflect"
	"testing"
)

// TestNewOllamaDefaultsToCloudURL pins the Ollama factory defaults: with no
// BaseURL the built-in descriptor's cloud URL is used, the API key is
// forwarded, and the completer identifies as "ollama".
func TestNewOllamaDefaultsToCloudURL(t *testing.T) {
	comp, err := NewOllama(Options{APIKey: "fake", CacheUsageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if comp == nil {
		t.Fatal("NewOllama returned nil completer")
	}
	if comp.Name() != "ollama" {
		t.Fatalf("NewOllama Name()=%q, want %q", comp.Name(), "ollama")
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewOllama must return *OpenAICompat, got %T", comp)
	}
	if client.baseURL != "https://ollama.com/v1" {
		t.Fatalf("NewOllama baseURL=%q, want %q", client.baseURL, "https://ollama.com/v1")
	}
	if client.apiKey != "fake" {
		t.Fatalf("NewOllama apiKey=%q, want %q", client.apiKey, "fake")
	}
	if client.name != "ollama" {
		t.Fatalf("NewOllama name=%q, want %q", client.name, "ollama")
	}
}

// TestNewOllamaHonorsOverride pins the BaseURL override path: an explicit
// base URL wins over the descriptor default, is not mutated by trailing-slash
// trimming (NewOpenAICompatWithOptions sets baseURL via
// strings.TrimRight(base, "/")), and the completer still identifies as
// "ollama".
func TestNewOllamaHonorsOverride(t *testing.T) {
	comp, err := NewOllama(Options{BaseURL: "https://example.test/v1", APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if comp == nil {
		t.Fatal("NewOllama returned nil completer")
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewOllama must return *OpenAICompat, got %T", comp)
	}
	if client.baseURL != "https://example.test/v1" {
		t.Fatalf("NewOllama baseURL=%q, want %q", client.baseURL, "https://example.test/v1")
	}
	if client.name != "ollama" {
		t.Fatalf("NewOllama name=%q, want %q", client.name, "ollama")
	}
	if client.apiKey != "fake" {
		t.Fatalf("NewOllama apiKey=%q, want %q", client.apiKey, "fake")
	}
}

// TestNewOllamaStripsKeyForLoopback pins the loopback guard: when the base
// URL points at a loopback address (127.0.0.1), NewOllama must drop the API
// key so it never leaks to a local endpoint. RED: the current factory passes
// the key through unchanged.
func TestNewOllamaStripsKeyForLoopback(t *testing.T) {
	comp, err := NewOllama(Options{BaseURL: "http://127.0.0.1:11434/v1", APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if comp == nil {
		t.Fatal("NewOllama returned nil completer")
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewOllama must return *OpenAICompat, got %T", comp)
	}
	if client.apiKey != "" {
		t.Fatalf("NewOllama apiKey=%q, want %q for loopback base URL", client.apiKey, "")
	}
}

// TestNewOllamaKeepsKeyForCloud pins the cloud path: when the base URL is a
// remote endpoint (ollama.com), NewOllama must forward the API key unchanged.
func TestNewOllamaKeepsKeyForCloud(t *testing.T) {
	comp, err := NewOllama(Options{BaseURL: "https://ollama.com/v1", APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if comp == nil {
		t.Fatal("NewOllama returned nil completer")
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewOllama must return *OpenAICompat, got %T", comp)
	}
	if client.apiKey != "fake" {
		t.Fatalf("NewOllama apiKey=%q, want %q", client.apiKey, "fake")
	}
}

// TestNewOllamaSetsNumCtxFromConfiguredContextWindow pins the fix for
// history silently vanishing on local ollama: without an explicit num_ctx,
// the daemon serves its own small default context window regardless of what
// mivia's prompt budgeting believes the model supports, so older turns get
// truncated server-side. When ContextWindowTokens is set, NewOllama must
// forward it as options.num_ctx in every request body.
func TestNewOllamaSetsNumCtxFromConfiguredContextWindow(t *testing.T) {
	comp, err := NewOllama(Options{BaseURL: "http://127.0.0.1:11434/v1", ContextWindowTokens: 262144})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewOllama must return *OpenAICompat, got %T", comp)
	}
	want := map[string]any{"options": map[string]any{"num_ctx": 262144}}
	if !reflect.DeepEqual(client.extraBody, want) {
		t.Fatalf("NewOllama extraBody=%#v, want %#v", client.extraBody, want)
	}
}

// TestNewOllamaOmitsNumCtxWhenContextWindowUnset pins the no-op default: an
// unrecognized or unconfigured model (ContextWindowTokens=0) must not send a
// num_ctx override, keeping the request body byte-identical to before this
// option existed.
func TestNewOllamaOmitsNumCtxWhenContextWindowUnset(t *testing.T) {
	comp, err := NewOllama(Options{BaseURL: "http://127.0.0.1:11434/v1"})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("NewOllama must return *OpenAICompat, got %T", comp)
	}
	if len(client.extraBody) != 0 {
		t.Fatalf("NewOllama extraBody=%#v, want empty", client.extraBody)
	}
}
