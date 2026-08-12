package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestFormatConfigShowModelPolicy(t *testing.T) {
	managed := formatConfigShow(&config.Resolved{ProviderName: "p", Model: "A", Models: []string{"A", "B"}})
	if !strings.Contains(managed, "models=A,B\nmodel_policy=restricted\n") {
		t.Fatalf("managed output = %q", managed)
	}
	unrestricted := formatConfigShow(&config.Resolved{ProviderName: "p", Model: "A"})
	if strings.Contains(unrestricted, "models=") || !strings.Contains(unrestricted, "model_policy=unrestricted\n") {
		t.Fatalf("unrestricted output = %q", unrestricted)
	}
}

func TestFormatConfigShowIncludesCatalogCapacityAndBudget(t *testing.T) {
	res := loadPickerConfig(t)
	res.MaxContextTokens = 991808
	got := formatConfigShow(res)
	if !strings.Contains(got, "model_catalog=deepseek/deepseek/one:128000,deepseek/deepseek/two:128000;openrouter/openai/gpt-4o-mini:128000\n") {
		t.Fatalf("catalog output = %q", got)
	}
	if !strings.Contains(got, "active_prompt_budget=991808\n") {
		t.Fatalf("budget output = %q", got)
	}
}

func TestFormatDoctorModelInfo(t *testing.T) {
	got := formatDoctorModelInfo(&config.Resolved{ProviderName: "deepseek", Model: "A", Models: []string{"A", "B"}})
	if !strings.Contains(got, "  models:     A, B\n") || strings.Contains(got, "note:") {
		t.Fatalf("doctor info = %q", got)
	}
}

func TestFormatDoctorModelInfoIncludesCatalogCapacity(t *testing.T) {
	res := loadPickerConfig(t)
	got := formatDoctorModelInfo(res)
	if !strings.Contains(got, "deepseek/deepseek/one:128000") || !strings.Contains(got, "openrouter/openai/gpt-4o-mini:128000") {
		t.Fatalf("doctor catalog = %q", got)
	}
}

func TestFormatConfigShowOllamaAPIKeyRequired(t *testing.T) {
	// A keyless loopback ollama profile performs no API-key auth, so config
	// show must not contradict doctor's "not required" verdict.
	loopback := formatConfigShow(&config.Resolved{ProviderName: "ollama", BaseURL: "http://127.0.0.1:11434/v1"})
	if !strings.Contains(loopback, "api_key_set=false\n") || !strings.Contains(loopback, "api_key_required=false\n") {
		t.Fatalf("loopback ollama output = %q", loopback)
	}
	cloud := formatConfigShow(&config.Resolved{ProviderName: "ollama", BaseURL: "https://ollama.com/v1"})
	if !strings.Contains(cloud, "api_key_required=true\n") {
		t.Fatalf("cloud ollama output = %q", cloud)
	}
}

// TestFormatConfigShowOllamaLoopbackFullOutput pins the complete ordered
// formatConfigShow output for a loopback ollama Resolved. The comparison is
// string-exact: a reorder, an added line, or a changed value in the emitted
// format fails the test.
func TestFormatConfigShowOllamaLoopbackFullOutput(t *testing.T) {
	got := formatConfigShow(&config.Resolved{
		ConfigPath:   "/cfg/mivia.toml",
		ProviderName: "ollama",
		Model:        "qwen3:8b",
		BaseURL:      "http://127.0.0.1:11434/v1",
		APIKeyEnv:    "OLLAMA_API_KEY",
	})
	want := `config_path=/cfg/mivia.toml
env_file=(none)
env_file_loaded=false
provider=ollama
model=qwen3:8b
model_policy=unrestricted
base_url=http://127.0.0.1:11434/v1
api_key_env=OLLAMA_API_KEY
api_key_set=false
api_key_required=false
`
	if got != want {
		t.Errorf("formatConfigShow(loopback ollama) mismatch.\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

// TestFormatConfigShowCloudOllamaRequiresAPIKey pins the full ordered
// formatConfigShow output for a cloud ollama Resolved (non-loopback
// base_url): api_key_required must be true in the exact emitted format.
func TestFormatConfigShowCloudOllamaRequiresAPIKey(t *testing.T) {
	got := formatConfigShow(&config.Resolved{
		ConfigPath:   "/cfg/mivia.toml",
		ProviderName: "ollama",
		Model:        "qwen3:8b",
		BaseURL:      "https://ollama.com/v1",
		APIKeyEnv:    "OLLAMA_API_KEY",
	})
	want := `config_path=/cfg/mivia.toml
env_file=(none)
env_file_loaded=false
provider=ollama
model=qwen3:8b
model_policy=unrestricted
base_url=https://ollama.com/v1
api_key_env=OLLAMA_API_KEY
api_key_set=false
api_key_required=true
`
	if got != want {
		t.Errorf("formatConfigShow(cloud ollama) mismatch.\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestConfigShowPromptBudgetAdvisoryShown(t *testing.T) {
	res := &config.Resolved{ProviderName: "deepseek", Model: "deepseek-v4-flash", MaxContextTokens: 616000}
	got := formatConfigShow(res)
	if !strings.Contains(got, "prompt_budget_advisory=unbounded (616000 tokens)") {
		t.Fatalf("advisory output = %q", got)
	}
	if !strings.Contains(got, "recommended 200000") {
		t.Fatalf("advisory missing recommendation = %q", got)
	}
}

func TestConfigShowPromptBudgetAdvisoryAbsentWhenCapped(t *testing.T) {
	res := &config.Resolved{ProviderName: "deepseek", Model: "deepseek-v4-flash", MaxContextTokens: 616000, MaxPromptTokens: intPtr(200000)}
	got := formatConfigShow(res)
	if strings.Contains(got, "prompt_budget_advisory") {
		t.Fatalf("capped output = %q", got)
	}
}
