package cliorchestrate

// doctor_active_model_window_test.go covers activeModelWindow's provider
// filter in doctor.go.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestActiveModelWindowSkipsOtherProvidersWithTheSameModelName pins the
// provider filter: model names are not unique across providers, so a catalog
// group belonging to a provider that is not bound must be skipped. Reading the
// first name match instead would report a foreign provider's context window as
// the active model's, quietly mis-sizing the doctor's prompt budget line.
func TestActiveModelWindowSkipsOtherProvidersWithTheSameModelName(t *testing.T) {
	res := &config.Resolved{ProviderName: "deepseek", Model: "shared-model"}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{
			Provider: "openrouter",
			Models:   []config.ModelSpec{{Name: "shared-model", ContextWindowTokens: 8000}},
		},
		{
			Provider: "deepseek",
			Models:   []config.ModelSpec{{Name: "shared-model", ContextWindowTokens: 128000}},
		},
	})
	if got := activeModelWindow(res); got != 128000 {
		t.Fatalf("activeModelWindow = %d, want 128000 (the bound provider's group)", got)
	}

	// A model that exists only under another provider is not described at all.
	res.Model = "openrouter-only"
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{
			Provider: "openrouter",
			Models:   []config.ModelSpec{{Name: "openrouter-only", ContextWindowTokens: 8000}},
		},
	})
	if got := activeModelWindow(res); got != 0 {
		t.Fatalf("activeModelWindow = %d, want 0 for a model absent from the bound provider", got)
	}
}
