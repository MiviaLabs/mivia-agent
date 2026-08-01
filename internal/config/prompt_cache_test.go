package config

import (
	"os"
	"path/filepath"
	"testing"
)

func minimalDeepSeekConfig(t *testing.T, extraProviderTOML string) (cfgPath string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	env := filepath.Join(dir, ".env")
	toml := `env_file = "` + filepath.ToSlash(env) + `"

[provider]
name = "deepseek"
` + extraProviderTOML + `

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 128000 }]
default_model = "deepseek-v4-pro"

[chat]
max_tokens = 8192
`
	if err := os.WriteFile(cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("DEEPSEEK_API_KEY=secret-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A config written before this feature existed has no [provider] prompt_cache
// key at all. It must still load successfully and default to "auto" - the
// defaulting must happen before Validate() runs, the same way every other
// Resolved field is defaulted inline in resolveLoaded.
func TestLoadDefaultsPromptCacheToAuto(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: minimalDeepSeekConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.PromptCache != "auto" {
		t.Fatalf("PromptCache = %q, want auto", res.PromptCache)
	}
}

func TestLoadPromptCacheOff(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: minimalDeepSeekConfig(t, "prompt_cache = \"off\"")})
	if err != nil {
		t.Fatal(err)
	}
	if res.PromptCache != "off" {
		t.Fatalf("PromptCache = %q, want off", res.PromptCache)
	}
}

func TestLoadRejectsInvalidPromptCache(t *testing.T) {
	_, err := Load(LoadOptions{ConfigPath: minimalDeepSeekConfig(t, "prompt_cache = \"sometimes\"")})
	if err == nil {
		t.Fatal("invalid prompt_cache value accepted")
	}
}

func TestResolvedValidateRejectsInvalidPromptCache(t *testing.T) {
	res := &Resolved{ProviderName: "deepseek", Model: "model", BaseURL: "https://example.test", APIKeyEnv: "KEY", PromptCache: "bogus"}
	if err := res.Validate(); err == nil {
		t.Fatal("invalid prompt_cache accepted by Validate")
	}
}
