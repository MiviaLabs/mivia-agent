package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A whitespace-only key value must count as missing everywhere (doctor, chat
// gate, provider runtime). config.Lookup returns ("   ", true) for it, so the
// APIKeySet computation has to trim before deciding.
func TestLoadWhitespaceOnlyKeyIsMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	toml := `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 128000 }]
default_model = "deepseek-v4-pro"
`
	if err := os.WriteFile(cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "   ")
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if res.APIKeySet {
		t.Fatalf("APIKeySet = true for a whitespace-only key")
	}
	if res.APIKey != "   " {
		t.Fatalf("APIKey = %q, want the raw value preserved for downstream trim checks", res.APIKey)
	}
}

// A whitespace-only TAVILY_API_KEY must resolve to an empty TavilyAPIKey so
// the search tool never treats the integration as configured.
func TestLoadWhitespaceOnlyTavilyKeyIsEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	toml := `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 128000 }]
default_model = "deepseek-v4-pro"
`
	if err := os.WriteFile(cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAVILY_API_KEY", "   ")
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if res.TavilyAPIKey != "" {
		t.Fatalf("TavilyAPIKey = %q, want empty for a whitespace-only key", res.TavilyAPIKey)
	}
}
