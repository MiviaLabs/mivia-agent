package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// TestConfigLoadLLMGatewayProviderNewChatTurn mirrors
// TestConfigLoadZAIProviderNewChatTurn: a config with an explicit local
// base_url round-trips through config.Load -> New -> ChatTurn against a fake
// httptest server, and the request body carries the hashed session user key,
// no cache_control markers, the model id verbatim, and reasoning_effort for
// the vetted openai dialect.
func TestConfigLoadLLMGatewayProviderNewChatTurn(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer integration-key" {
			t.Errorf("authorization=%q", got)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decoding request body: %v\nbody: %s", err, raw)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "integrated"}, "finish_reason": "stop"}}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	configPath := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(envPath, []byte("LLMGATEWAY_API_KEY=integration-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := "env_file = \"" + filepath.ToSlash(envPath) + "\"\n\n[provider]\nname = \"llmgateway\"\n\n" +
		"[providers.llmgateway]\nmodels = [{name=\"deepseek-v4-pro\", context_window_tokens=1100000}]\n" +
		"base_url = \"" + srv.URL + "/v1\"\n\n[chat]\nmax_tokens=8192\n"
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Load(config.LoadOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	completer, err := New(resolved)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		Model:          resolved.Model,
		Messages:       []Message{{Role: RoleUser, Content: "hi"}},
		SessionID:      "session-a",
		ReasoningLevel: reasoning.High,
	}
	response, err := completer.ChatTurn(context.Background(), req)
	if err != nil || response.Content != "integrated" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, has := gotBody["cache_control"]; has {
		t.Fatalf("body must not carry cache_control, got %v", gotBody)
	}
	if raw, _ := json.Marshal(gotBody); strings.Contains(string(raw), "cache_control") {
		t.Fatalf("body must not carry cache_control anywhere, got %s", raw)
	}
	user, ok := gotBody["user"].(string)
	if !ok || user == "" || user == "session-a" {
		t.Fatalf("expected a hashed non-empty user field, got %v", gotBody["user"])
	}
	if got := gotBody["model"]; got != "deepseek-v4-pro" {
		t.Fatalf("model=%v, want deepseek-v4-pro sent verbatim", got)
	}
	if got := gotBody["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort=%v, want %q", got, "high")
	}
}

// TestConfigLoadLLMGatewayProviderAppliesDescriptorDefaults confirms the
// descriptor's default base_url and api_key_env apply when a config omits
// them; the real https://api.llmgateway.io/v1 cannot round-trip against
// httptest, so this checks the resolved config values only.
func TestConfigLoadLLMGatewayProviderAppliesDescriptorDefaults(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	configPath := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(envPath, []byte("LLMGATEWAY_API_KEY=integration-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := "env_file = \"" + filepath.ToSlash(envPath) + "\"\n\n[provider]\nname = \"llmgateway\"\n\n" +
		"[providers.llmgateway]\nmodels = [{name=\"deepseek-v4-pro\", context_window_tokens=1100000}]\n\n[chat]\nmax_tokens=8192\n"
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Load(config.LoadOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.BaseURL != "https://api.llmgateway.io/v1" {
		t.Fatalf("BaseURL=%q, want the descriptor default", resolved.BaseURL)
	}
	if resolved.APIKeyEnv != "LLMGATEWAY_API_KEY" {
		t.Fatalf("APIKeyEnv=%q, want the descriptor default", resolved.APIKeyEnv)
	}
	if _, err := New(resolved); err != nil {
		t.Fatalf("New() with descriptor defaults: %v", err)
	}
}

// TestConfigLoadLLMGatewayResolvesPerModelReasoningDialect proves the
// end-to-end wiring config.Load -> provider.NewForProvider -> NewLLMGateway:
// two sibling models declared under the SAME [providers.llmgateway] table
// must get independent reasoning-replay contracts. The model that opts into
// DeepSeek's thinking_effort dialect gets DeepSeek's documented
// reject/repair gate; its sibling with no override keeps the provider's
// plain openai default and must NOT.
func TestConfigLoadLLMGatewayResolvesPerModelReasoningDialect(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	configPath := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(envPath, []byte("LLMGATEWAY_API_KEY=integration-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := "env_file = \"" + filepath.ToSlash(envPath) + "\"\n\n[provider]\nname = \"llmgateway\"\n\n" +
		"[providers.llmgateway]\n" +
		"default_model = \"runware/deepseek-v4-flash\"\n" +
		"models = [\n" +
		"  {name=\"runware/deepseek-v4-flash\", context_window_tokens=1000000, reasoning_efforts=[\"high\",\"max\"], reasoning=\"high\", reasoning_dialect=\"thinking_effort\"},\n" +
		"  {name=\"runware/gpt-oss-120b\", context_window_tokens=131072, reasoning_efforts=[\"low\",\"medium\",\"high\"], reasoning=\"high\"},\n" +
		"]\n\n[chat]\nmax_tokens=8192\n"
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Load(config.LoadOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}

	deepseekResolved := *resolved
	deepseekResolved.Model = "runware/deepseek-v4-flash"
	deepseekCompleter, err := New(&deepseekResolved)
	if err != nil {
		t.Fatal(err)
	}
	deepseekPolicy := ReasoningPolicyFor(deepseekCompleter)
	if !deepseekPolicy.RequiresReplay || !deepseekPolicy.RejectReasoningLess {
		t.Fatalf("runware/deepseek-v4-flash policy=%+v, want both true", deepseekPolicy)
	}

	gptOSSResolved := *resolved
	gptOSSResolved.Model = "runware/gpt-oss-120b"
	gptOSSCompleter, err := New(&gptOSSResolved)
	if err != nil {
		t.Fatal(err)
	}
	gptOSSPolicy := ReasoningPolicyFor(gptOSSCompleter)
	if !gptOSSPolicy.RequiresReplay {
		t.Fatalf("runware/gpt-oss-120b policy=%+v, want RequiresReplay true (safe no-op)", gptOSSPolicy)
	}
	if gptOSSPolicy.RejectReasoningLess {
		t.Fatalf("runware/gpt-oss-120b policy=%+v, want RejectReasoningLess false: it must not inherit its sibling model's DeepSeek-only contract", gptOSSPolicy)
	}
}

// TestLLMGatewayPinsProviderOnWireOnlyForThinkingEffortModel proves the
// X-No-Fallback header actually reaches the HTTP request, and only for the
// sibling model whose resolved dialect is thinking_effort - the same
// two-model config as TestConfigLoadLLMGatewayResolvesPerModelReasoningDialect,
// this time asserting the wire header rather than the policy struct.
func TestLLMGatewayPinsProviderOnWireOnlyForThinkingEffortModel(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-No-Fallback")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}, "finish_reason": "stop"}}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	configPath := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(envPath, []byte("LLMGATEWAY_API_KEY=integration-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := "env_file = \"" + filepath.ToSlash(envPath) + "\"\n\n[provider]\nname = \"llmgateway\"\n\n" +
		"[providers.llmgateway]\n" +
		"default_model = \"runware/deepseek-v4-flash\"\n" +
		"base_url = \"" + srv.URL + "/v1\"\n" +
		"models = [\n" +
		"  {name=\"runware/deepseek-v4-flash\", context_window_tokens=1000000, reasoning_efforts=[\"high\",\"max\"], reasoning=\"high\", reasoning_dialect=\"thinking_effort\"},\n" +
		"  {name=\"runware/gpt-oss-120b\", context_window_tokens=131072, reasoning_efforts=[\"low\",\"medium\",\"high\"], reasoning=\"high\"},\n" +
		"]\n\n[chat]\nmax_tokens=8192\n"
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Load(config.LoadOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}

	deepseekResolved := *resolved
	deepseekResolved.Model = "runware/deepseek-v4-flash"
	deepseekCompleter, err := New(&deepseekResolved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deepseekCompleter.ChatTurn(context.Background(), Request{Model: "runware/deepseek-v4-flash", Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "true" {
		t.Fatalf("X-No-Fallback=%q for the thinking_effort model, want %q", gotHeader, "true")
	}

	gptOSSResolved := *resolved
	gptOSSResolved.Model = "runware/gpt-oss-120b"
	gptOSSCompleter, err := New(&gptOSSResolved)
	if err != nil {
		t.Fatal(err)
	}
	gotHeader = "unset"
	if _, err := gptOSSCompleter.ChatTurn(context.Background(), Request{Model: "runware/gpt-oss-120b", Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "" {
		t.Fatalf("X-No-Fallback=%q for the sibling non-thinking model, want it unset", gotHeader)
	}
}

// TestLLMGatewayReplaysReasoningContentWithoutDroppingReasoningLessTurns pins
// the fix for a DeepSeek-family model behind the gateway losing its own
// prior chain-of-thought: RequiresReasoningReplay must be on (so the terminal
// tool-call turn's reasoning_content reaches the wire), while
// RejectReasoningLessToolTurns must stay off (so an OLDER tool-call turn from
// a non-thinking model with no reasoning_content is NOT silently dropped -
// that repair is DeepSeek-only and would corrupt history for any other model
// sharing this gateway client).
func TestLLMGatewayReplaysReasoningContentWithoutDroppingReasoningLessTurns(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decoding request body: %v\nbody: %s", err, raw)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}, "finish_reason": "stop"}}})
	}))
	defer srv.Close()
	comp, err := NewLLMGateway(Options{BaseURL: srv.URL, APIKey: "fake-key"})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		Model: "runware/deepseek-v4-flash",
		Messages: []Message{
			{Role: RoleUser, Content: "step 1"},
			// An older, non-terminal tool-call turn with no reasoning_content -
			// as a non-thinking model on this gateway would produce. Must
			// survive: only DeepSeek's RejectReasoningLessToolTurns gate drops
			// these, and llmgateway never sets it.
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Type: "function"}}},
			{Role: RoleTool, ToolCallID: "call_1", Content: "result 1"},
			{Role: RoleUser, Content: "step 2"},
			// The terminal exchange, carrying reasoning_content like a DeepSeek
			// thinking-mode turn would.
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_2", Type: "function"}}, ReasoningContent: "because X implies Y"},
			{Role: RoleTool, ToolCallID: "call_2", Content: "result 2"},
		},
	}
	if _, err := comp.ChatTurn(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(gotBody)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "because X implies Y") {
		t.Fatalf("expected the terminal turn's reasoning_content on the wire, got %s", body)
	}
	if !strings.Contains(body, "call_1") {
		t.Fatalf("the older reasoning-less tool-call turn must not be dropped, got %s", body)
	}
}

// TestLLMGatewayErrorEnvelopeParsesThroughSharedParser pins that a
// documented OpenAI-shaped error envelope from the gateway is classified by
// the shared openaiErrorParser; llmgateway adds no per-provider parser.
func TestLLMGatewayErrorEnvelopeParsesThroughSharedParser(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
	}))
	defer srv.Close()
	comp, err := NewLLMGateway(Options{BaseURL: srv.URL, APIKey: "fake-key"})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Model: "deepseek-v4-pro", Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	_, err = comp.ChatTurn(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "rate limited (HTTP 429)") {
		t.Fatalf("err=%v, want a rate-limit classification from the shared parser", err)
	}
}
