package config

import (
	"strings"
	"testing"
)

// resolveProvider and normalizeProviderConfigs coverage: default-model
// resolution (with NormalizeModelName), --model override validation, and
// registry-descriptor defaulting of BaseURL/APIKeyEnv, both in
// resolveProvider (the active provider only) and normalizeProviderConfigs
// (every declared provider, looped).

func TestResolveProvider_DefaultModelIsNormalizedAndSelected(t *testing.T) {
	file := File{
		Provider: ProviderSection{Name: "deepseek"},
		Providers: map[string]ProviderConfig{
			"deepseek": {
				Models: []ModelSpec{
					{Name: "deepseek-v4-flash"},
					{Name: "deepseek-v4-pro"},
				},
				// Leading/trailing whitespace forces NormalizeModelName to do
				// real work, not just a no-op comparison.
				DefaultModel: "  deepseek-v4-pro  ",
			},
		},
	}
	name, pc, model, err := resolveProvider(file, LoadOptions{})
	if err != nil {
		t.Fatalf("resolveProvider() error = %v", err)
	}
	if name != "deepseek" {
		t.Fatalf("provider = %q, want deepseek", name)
	}
	if model != "deepseek-v4-pro" {
		t.Fatalf("model = %q, want the normalized default_model", model)
	}
	if pc.DefaultModel != "deepseek-v4-pro" {
		t.Fatalf("pc.DefaultModel = %q, want normalized in place", pc.DefaultModel)
	}
	// The registry descriptor fills BaseURL/APIKeyEnv since the config left
	// both empty.
	if pc.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("pc.BaseURL = %q, want the deepseek registry default", pc.BaseURL)
	}
	if pc.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("pc.APIKeyEnv = %q, want the deepseek registry default", pc.APIKeyEnv)
	}
}

func TestResolveProvider_InvalidDefaultModelIsRejected(t *testing.T) {
	file := File{
		Provider: ProviderSection{Name: "deepseek"},
		Providers: map[string]ProviderConfig{
			"deepseek": {
				Models:       []ModelSpec{{Name: "deepseek-v4-flash"}},
				DefaultModel: "\x1b]52;c;unsafe",
			},
		},
	}
	_, _, _, err := resolveProvider(file, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "default_model is invalid") {
		t.Fatalf("resolveProvider() error = %v, want default_model is invalid", err)
	}
}

func TestResolveProvider_DefaultModelNotListedIsRejected(t *testing.T) {
	file := File{
		Provider: ProviderSection{Name: "deepseek"},
		Providers: map[string]ProviderConfig{
			"deepseek": {
				Models:       []ModelSpec{{Name: "deepseek-v4-flash"}},
				DefaultModel: "not-listed",
			},
		},
	}
	_, _, _, err := resolveProvider(file, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "default_model is not in models") {
		t.Fatalf("resolveProvider() error = %v, want default_model is not in models", err)
	}
}

func TestResolveProvider_ModelOverrideIsValidatedAgainstModels(t *testing.T) {
	file := File{
		Provider: ProviderSection{Name: "deepseek"},
		Providers: map[string]ProviderConfig{
			"deepseek": {
				Models: []ModelSpec{
					{Name: "deepseek-v4-flash"},
					{Name: "deepseek-v4-pro"},
				},
			},
		},
	}
	// Valid override wins over the first-model default.
	_, _, model, err := resolveProvider(file, LoadOptions{ModelOverride: "  deepseek-v4-pro  "})
	if err != nil {
		t.Fatalf("resolveProvider() error = %v", err)
	}
	if model != "deepseek-v4-pro" {
		t.Fatalf("model = %q, want the override", model)
	}

	// Invalid (control-character) override is rejected before the
	// membership check.
	_, _, _, err = resolveProvider(file, LoadOptions{ModelOverride: "\x1b]52;c;unsafe"})
	if err == nil || !strings.Contains(err.Error(), "--model is invalid") {
		t.Fatalf("resolveProvider() error = %v, want --model is invalid", err)
	}

	// Override not present in the declared catalog.
	_, _, _, err = resolveProvider(file, LoadOptions{ModelOverride: "no-such-model"})
	if err == nil || !strings.Contains(err.Error(), "--model is not in models") {
		t.Fatalf("resolveProvider() error = %v, want --model is not in models", err)
	}
}

func TestResolveProvider_DefaultsBaseURLAndAPIKeyEnvFromRegistry(t *testing.T) {
	file := File{
		Provider: ProviderSection{Name: "zai"},
		Providers: map[string]ProviderConfig{
			"zai": {
				Models: []ModelSpec{{Name: "glm-5.2"}},
				// BaseURL/APIKeyEnv deliberately left empty.
			},
		},
	}
	_, pc, _, err := resolveProvider(file, LoadOptions{})
	if err != nil {
		t.Fatalf("resolveProvider() error = %v", err)
	}
	if pc.BaseURL != "https://api.z.ai/api/paas/v4" {
		t.Fatalf("pc.BaseURL = %q, want the zai registry default", pc.BaseURL)
	}
	if pc.APIKeyEnv != "ZAI_API_KEY" {
		t.Fatalf("pc.APIKeyEnv = %q, want the zai registry default", pc.APIKeyEnv)
	}
}

func TestResolveProvider_ExplicitBaseURLAndAPIKeyEnvAreNotOverridden(t *testing.T) {
	file := File{
		Provider: ProviderSection{Name: "deepseek"},
		Providers: map[string]ProviderConfig{
			"deepseek": {
				Models:    []ModelSpec{{Name: "custom-model"}},
				BaseURL:   "https://custom.example.test/v1",
				APIKeyEnv: "CUSTOM_KEY_ENV",
			},
		},
	}
	_, pc, _, err := resolveProvider(file, LoadOptions{})
	if err != nil {
		t.Fatalf("resolveProvider() error = %v", err)
	}
	if pc.BaseURL != "https://custom.example.test/v1" {
		t.Fatalf("pc.BaseURL = %q, want the configured value preserved", pc.BaseURL)
	}
	if pc.APIKeyEnv != "CUSTOM_KEY_ENV" {
		t.Fatalf("pc.APIKeyEnv = %q, want the configured value preserved", pc.APIKeyEnv)
	}
}

func TestNormalizeProviderConfigs_DefaultsEachProviderIndependently(t *testing.T) {
	file := File{
		Providers: map[string]ProviderConfig{
			"deepseek": {
				Models: []ModelSpec{{Name: "deepseek-v4-flash", ContextWindowTokens: 128000}},
				// BaseURL/APIKeyEnv left empty; must default per-provider.
			},
			"zai": {
				Models: []ModelSpec{{Name: "glm-5.2", ContextWindowTokens: 128000}},
			},
			"anthropic": {
				Models:    []ModelSpec{{Name: "claude", ContextWindowTokens: 128000}},
				BaseURL:   "https://proxy.example.test/v1",
				APIKeyEnv: "PROXY_KEY",
			},
		},
	}
	if err := normalizeProviderConfigs(&file, 0); err != nil {
		t.Fatalf("normalizeProviderConfigs() error = %v", err)
	}
	ds := file.Providers["deepseek"]
	if ds.BaseURL != "https://api.deepseek.com/v1" || ds.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("deepseek defaults = %+v, want registry defaults applied", ds)
	}
	zai := file.Providers["zai"]
	if zai.BaseURL != "https://api.z.ai/api/paas/v4" || zai.APIKeyEnv != "ZAI_API_KEY" {
		t.Fatalf("zai defaults = %+v, want registry defaults applied", zai)
	}
	anthropic := file.Providers["anthropic"]
	if anthropic.BaseURL != "https://proxy.example.test/v1" || anthropic.APIKeyEnv != "PROXY_KEY" {
		t.Fatalf("anthropic config = %+v, want the operator's explicit values preserved (not overwritten by the registry default)", anthropic)
	}
	// Model normalization ran too: names are unchanged here (already
	// trimmed, valid) but the models slice must survive the loop with the
	// same identity.
	if len(ds.Models) != 1 || ds.Models[0].Name != "deepseek-v4-flash" {
		t.Fatalf("deepseek models = %+v, want normalized models preserved", ds.Models)
	}
}

func TestNormalizeProviderConfigs_UnknownProviderIsRejected(t *testing.T) {
	file := File{
		Providers: map[string]ProviderConfig{
			"not-a-real-provider": {Models: []ModelSpec{{Name: "x", ContextWindowTokens: 128000}}},
		},
	}
	if err := normalizeProviderConfigs(&file, 0); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("normalizeProviderConfigs() error = %v, want unknown provider", err)
	}
}

func TestResolveProvider_NilProvidersMapIsInitialized(t *testing.T) {
	file := File{
		Provider:  ProviderSection{Name: "deepseek"},
		Providers: nil,
	}
	_, _, _, err := resolveProvider(file, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("resolveProvider() error = %v, want the not-configured error (nil Providers map must not panic)", err)
	}
}

func TestResolveProvider_UnknownProviderNameIsRejected(t *testing.T) {
	file := File{Provider: ProviderSection{Name: "not-a-real-provider"}}
	_, _, _, err := resolveProvider(file, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("resolveProvider() error = %v, want unknown provider", err)
	}
}

func TestNormalizeProviderConfigs_WhitespaceOnlyNameIsRejected(t *testing.T) {
	file := File{
		Providers: map[string]ProviderConfig{
			"   ": {Models: []ModelSpec{{Name: "x", ContextWindowTokens: 128000}}},
		},
	}
	if err := normalizeProviderConfigs(&file, 0); err == nil || !strings.Contains(err.Error(), "provider name is empty") {
		t.Fatalf("normalizeProviderConfigs() error = %v, want provider name is empty", err)
	}
}

func TestNormalizeProviderConfigs_NilProvidersMapIsInitialized(t *testing.T) {
	file := File{Providers: nil}
	if err := normalizeProviderConfigs(&file, 0); err != nil {
		t.Fatalf("normalizeProviderConfigs() error = %v, want nil for an empty catalog", err)
	}
	if file.Providers == nil {
		t.Fatal("normalizeProviderConfigs() left file.Providers nil, want an initialized empty map")
	}
}

func TestNormalizeProviderConfigs_CaseCollisionIsRejected(t *testing.T) {
	file := File{
		Providers: map[string]ProviderConfig{
			"deepseek": {Models: []ModelSpec{{Name: "a", ContextWindowTokens: 128000}}},
			"DeepSeek": {Models: []ModelSpec{{Name: "b", ContextWindowTokens: 128000}}},
		},
	}
	if err := normalizeProviderConfigs(&file, 0); err == nil || !strings.Contains(err.Error(), "collide by case") {
		t.Fatalf("normalizeProviderConfigs() error = %v, want collide by case", err)
	}
}
