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

func TestSubagentConfigDefaults(t *testing.T) {
	res, err := Load(LoadOptions{AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.MaxWorkers != 4 {
		t.Fatalf("MaxWorkers: got %d want 4", res.Subagents.MaxWorkers)
	}
	if res.Subagents.MaxDepth != 3 {
		t.Fatalf("MaxDepth: got %d want 3", res.Subagents.MaxDepth)
	}
	if res.Subagents.MaxFanout != 16 {
		t.Fatalf("MaxFanout: got %d want 16", res.Subagents.MaxFanout)
	}
	if res.Subagents.DefaultTimeout != 0 {
		t.Fatalf("DefaultTimeout: got %d want 0", res.Subagents.DefaultTimeout)
	}
	if res.Subagents.SystemPrompt == "" {
		t.Fatal("SystemPrompt should have a default")
	}
}

func TestSubagentConfigFromTOML(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(cfg, []byte(`[subagents]
max_workers = 8
max_depth = 5
max_fanout = 32
default_timeout_seconds = 120
`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(LoadOptions{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.MaxWorkers != 8 {
		t.Fatalf("MaxWorkers: got %d want 8", res.Subagents.MaxWorkers)
	}
	if res.Subagents.MaxDepth != 5 {
		t.Fatalf("MaxDepth: got %d want 5", res.Subagents.MaxDepth)
	}
	if res.Subagents.MaxFanout != 32 {
		t.Fatalf("MaxFanout: got %d want 32", res.Subagents.MaxFanout)
	}
	if res.Subagents.DefaultTimeout != 120 {
		t.Fatalf("DefaultTimeout: got %v want 120s", res.Subagents.DefaultTimeout)
	}
}
