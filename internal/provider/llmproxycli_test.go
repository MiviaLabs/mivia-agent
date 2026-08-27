package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// End-to-end through the real config-resolution path (not a direct
// NewLLMProxyCLI(Options{...}) call): a config.Resolved whose llmproxycli
// runtime declares a model with ReasoningDialect DialectAnthropicAdaptive
// must reach NewForProvider and come out as a dispatcher recognizing that
// model name, proving anthropicNativeModelsFor's scan of runtime.Models
// actually wires through Options.AnthropicNativeModels correctly.
func TestNewForProviderLLMProxyCLIWiresAnthropicNativeModels(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "llmproxycli",
		Model:        "claude-sonnet-5",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"llmproxycli": {
				ProviderName: "llmproxycli",
				BaseURL:      "http://127.0.0.1:8317/v1",
				APIKeyEnv:    "CLIPROXY_API_KEY",
				APIKey:       "fake-key",
				APIKeySet:    true,
				Models: []config.ModelSpec{
					{Name: "claude-sonnet-5", ReasoningDialect: reasoning.DialectAnthropicAdaptive},
					{Name: "gemini-3.7-flash-high"},
				},
			},
		},
	}
	comp, err := NewForProvider(res, "llmproxycli")
	if err != nil {
		t.Fatalf("NewForProvider: %v", err)
	}
	dispatch, ok := comp.(*llmProxyDispatchCompleter)
	if !ok {
		t.Fatalf("comp = %T, want *llmProxyDispatchCompleter", comp)
	}
	if !dispatch.nativeModels["claude-sonnet-5"] {
		t.Fatal("claude-sonnet-5 must be recognized as a native-Anthropic model")
	}
	if dispatch.nativeModels["gemini-3.7-flash-high"] {
		t.Fatal("gemini-3.7-flash-high must NOT be recognized as a native-Anthropic model")
	}
}

// With no AnthropicNativeModels, NewLLMProxyCLI returns a plain *OpenAICompat
// unchanged - byte-identical to every existing config that never opts in.
func TestNewLLMProxyCLIWithNoNativeModelsReturnsPlainOpenAICompat(t *testing.T) {
	comp, err := NewLLMProxyCLI(Options{APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := comp.(*OpenAICompat); !ok {
		t.Fatalf("comp = %T, want *OpenAICompat when no model opts into native Anthropic routing", comp)
	}
}

// A model named in AnthropicNativeModels routes through the native Anthropic
// wire format (system/messages/content-blocks, no OpenAI chat/completions
// envelope), reusing llmproxycli's own base URL and API key - not a separate
// anthropic provider. Every other model on the same provider still speaks
// OpenAI-compat, from the same Completer instance.
func TestNewLLMProxyCLIDispatchesOptedInModelToNativeAnthropic(t *testing.T) {
	var anthropicHit, compatHit bool
	srv := httptest.NewServer(dispatchTestProxyHandler(t, &anthropicHit, &compatHit))
	defer srv.Close()

	comp, err := NewLLMProxyCLI(Options{
		APIKey:                "fake",
		BaseURL:               srv.URL + "/v1",
		AnthropicNativeModels: []string{"claude-sonnet-5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatch, ok := comp.(*llmProxyDispatchCompleter)
	if !ok {
		t.Fatalf("comp = %T, want *llmProxyDispatchCompleter when a model opts in", comp)
	}
	if dispatch.Name() != "llmproxycli" {
		t.Fatalf("Name() = %q, want llmproxycli (the config-facing provider identity, unchanged by internal routing)", dispatch.Name())
	}

	// The opted-in Claude model, with the exact combination that caused the
	// original bug report: a non-default temperature plus active reasoning.
	temp := 0.0
	nativeResp, err := comp.ChatTurn(context.Background(), Request{
		Model:          "claude-sonnet-5",
		Messages:       []Message{{Role: RoleUser, Content: "hi"}},
		Temperature:    &temp,
		ReasoningLevel: reasoning.Medium,
	})
	if err != nil {
		t.Fatalf("ChatTurn(native model): %v", err)
	}
	if nativeResp.Content != "native ok" {
		t.Fatalf("native response Content = %q, want %q", nativeResp.Content, "native ok")
	}
	if !anthropicHit {
		t.Fatal("the opted-in model must reach /v1/messages")
	}

	// A different, non-opted-in model on the SAME Completer instance still
	// goes through the unchanged OpenAI-compat path.
	compatResp, err := comp.ChatTurn(context.Background(), Request{
		Model:    "gemini-3.7-flash-high",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn(compat model): %v", err)
	}
	if compatResp.Content != "compat ok" {
		t.Fatalf("compat response Content = %q, want %q", compatResp.Content, "compat ok")
	}
	if !compatHit {
		t.Fatal("a non-opted-in model must still reach /v1/chat/completions")
	}
}

// dispatchTestProxyHandler is the fake proxy backing
// TestNewLLMProxyCLIDispatchesOptedInModelToNativeAnthropic: it serves both
// wire formats on one host, asserting the native path's request shape (in
// particular, that temperature never reaches the wire - see that test's
// comment for why) and recording which path each request hit.
func dispatchTestProxyHandler(t *testing.T, anthropicHit, compatHit *bool) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			*anthropicHit = true
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, hasMessages := body["messages"]; !hasMessages {
				t.Errorf("native path request missing Anthropic-shaped messages field: %#v", body)
			}
			if _, hasChoices := body["reasoning_effort"]; hasChoices {
				t.Errorf("native path request must not carry OpenAI-compat reasoning_effort: %#v", body)
			}
			// The exact reason this feature exists: the request carries a
			// non-default temperature (from a [chat]-wide setting) and
			// active reasoning - Anthropic 400s on a non-default
			// temperature outright, so it must never reach the wire here,
			// regardless of what the caller's Request.Temperature was set
			// to. Step-5 bug audit caught a version of this fix that still
			// forwarded it, which would have reproduced the exact bug
			// report this feature exists to close.
			if _, hasTemperature := body["temperature"]; hasTemperature {
				t.Errorf("native path request must never carry temperature - Anthropic 400s on a non-default value: %#v", body)
			}
			if got := r.Header.Get("anthropic-version"); got != anthropicAPIVersion {
				t.Errorf("native path anthropic-version header = %q, want %q", got, anthropicAPIVersion)
			}
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"native ok"}],"stop_reason":"end_turn","usage":{}}`))
		case "/v1/chat/completions":
			*compatHit = true
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"compat ok"},"finish_reason":"stop"}]}`))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}
