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
