package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsDeepSeekFlash(t *testing.T) {
	t.Setenv("MIVIA_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
	// Force missing config path via empty search: use AllowMissingConfig
	res, err := Load(LoadOptions{AllowMissingConfig: true, ConfigPath: ""})
	// Clear MIVIA_CONFIG for search - set to nonexistent so we need allow missing
	// Actually ConfigPath empty and MIVIA_CONFIG points missing will fail FirstExisting
	// AllowMissingConfig with no file found:
	_ = os.Unsetenv("MIVIA_CONFIG")
	res, err = Load(LoadOptions{AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.ProviderName != DeepSeekName {
		t.Fatalf("provider: %s", res.ProviderName)
	}
	if res.Model != DeepSeekDefaultModel {
		t.Fatalf("model: %s want %s", res.Model, DeepSeekDefaultModel)
	}
	if res.APIKeyEnv != DeepSeekAPIKeyEnv {
		t.Fatalf("key env: %s", res.APIKeyEnv)
	}
}

func TestLoadTOMLAndEnv(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	env := filepath.Join(dir, ".env")
	toml := `
[provider]
name = "deepseek"
	env_file = "` + filepath.ToSlash(env) + `"

[providers.deepseek]
model = "deepseek-v4-pro"
`
	if err := os.WriteFile(cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("DEEPSEEK_API_KEY=secret-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != DeepSeekProModel {
		t.Fatalf("model: %s", res.Model)
	}
	if !res.APIKeySet || res.APIKey != "secret-key" {
		t.Fatalf("api key not resolved")
	}
}

func TestModelOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[provider]
name = "deepseek"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg, ModelOverride: DeepSeekProModel})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != DeepSeekProModel {
		t.Fatalf("model: %s", res.Model)
	}
}

func TestRejectHTTPBaseURL(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[provider]
name = "deepseek"
[providers.deepseek]
base_url = "http://127.0.0.1:9/v1"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(LoadOptions{ConfigPath: cfg})
	if err == nil {
		t.Fatal("expected https error")
	}
}
