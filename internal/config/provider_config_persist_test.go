package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestUpdateProviderDefaultModel_NewFile(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "subdir", "mivia.toml")

	if err := UpdateProviderDefaultModel(cfgPath, "ollama", "llama3.2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var raw struct {
		Providers map[string]struct {
			DefaultModel string `toml:"default_model"`
		} `toml:"providers"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	p, ok := raw.Providers["ollama"]
	if !ok {
		t.Fatalf("expected ollama provider in config, got: %s", string(data))
	}
	if p.DefaultModel != "llama3.2" {
		t.Errorf("default_model = %q, want %q", p.DefaultModel, "llama3.2")
	}
}

func TestUpdateProviderDefaultModel_ExistingProvider(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "mivia.toml")

	initial := `
[chat]
max_tokens = 8192

[providers.openrouter]
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
default_model = "anthropic/claude-sonnet-4"
`
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	if err := UpdateProviderDefaultModel(cfgPath, "openrouter", "openai/gpt-5"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	chat, ok := raw["chat"].(map[string]any)
	if !ok || chat["max_tokens"] == nil {
		t.Errorf("expected [chat] section preserved, got: %s", string(data))
	}

	providers := raw["providers"].(map[string]any)
	openrouter := providers["openrouter"].(map[string]any)
	if openrouter["default_model"] != "openai/gpt-5" {
		t.Errorf("expected default_model = openai/gpt-5, got %v", openrouter["default_model"])
	}
	if openrouter["base_url"] != "https://openrouter.ai/api/v1" {
		t.Errorf("expected base_url preserved, got %v", openrouter["base_url"])
	}
}

func TestUpdateProviderDefaultModel_Validation(t *testing.T) {
	if err := UpdateProviderDefaultModel("", "p", "m"); err == nil || !strings.Contains(err.Error(), "config path is empty") {
		t.Errorf("expected config path is empty error, got %v", err)
	}
	if err := UpdateProviderDefaultModel("/path", "", "m"); err == nil || !strings.Contains(err.Error(), "provider name is empty") {
		t.Errorf("expected provider name is empty error, got %v", err)
	}
	if err := UpdateProviderDefaultModel("/path", "p", ""); err == nil || !strings.Contains(err.Error(), "model name is empty") {
		t.Errorf("expected model name is empty error, got %v", err)
	}
}
