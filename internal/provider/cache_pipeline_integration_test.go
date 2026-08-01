package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// End-to-end: config.Load -> provider.New -> ChatTurn against a real
// httptest server returning DeepSeek-shaped cache usage fields, exercising
// the full CacheUsageEnabled wiring path (config.Resolved.PromptCache ->
// provider.Options -> CompatOptions -> OpenAICompat).
func TestConfigLoadProviderNewChatTurnCapturesCacheUsage(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "integrated"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "prompt_cache_hit_tokens": 80, "prompt_cache_miss_tokens": 20},
		})
	}))
	defer srv.Close()
	resolved := loadDeepSeekConfigForCacheTest(t, srv.URL, "")

	completer, err := New(resolved)
	if err != nil {
		t.Fatal(err)
	}
	response, err := completer.ChatTurn(context.Background(), Request{Model: resolved.Model, Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := CacheUsage{Reported: true, Style: CacheStyleImplicit, InputTokens: 100, CachedInputTokens: 80, CacheWriteTokens: 0}
	if response.CacheUsage != want {
		t.Fatalf("CacheUsage = %+v, want %+v", response.CacheUsage, want)
	}
}

// prompt_cache = "off" reaches all the way down to OpenAICompat: the server
// still sends cache fields, but they must not be reported.
func TestConfigLoadProviderNewChatTurnPromptCacheOffDisablesCapture(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "integrated"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "prompt_cache_hit_tokens": 80},
		})
	}))
	defer srv.Close()
	resolved := loadDeepSeekConfigForCacheTest(t, srv.URL, "prompt_cache = \"off\"")

	completer, err := New(resolved)
	if err != nil {
		t.Fatal(err)
	}
	response, err := completer.ChatTurn(context.Background(), Request{Model: resolved.Model, Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.CacheUsage.Reported {
		t.Fatalf("prompt_cache=off must disable capture, got %+v", response.CacheUsage)
	}
}

func loadDeepSeekConfigForCacheTest(t *testing.T, baseURL, extraProviderTOML string) *config.Resolved {
	t.Helper()
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	configPath := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(envPath, []byte("DEEPSEEK_API_KEY=integration-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := "env_file = \"" + filepath.ToSlash(envPath) + "\"\n\n[provider]\nname = \"deepseek\"\n" + extraProviderTOML +
		"\n\n[providers.deepseek]\nmodels = [{name=\"deepseek-v4-flash\", context_window_tokens=128000}]\nbase_url = \"" + baseURL + "\"\n\n[chat]\nmax_tokens=8192\n"
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Load(config.LoadOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
