package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCatalogConfig(t *testing.T, body, env string) string {
	t.Helper()
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mivia.toml")
	contents := "env_file = \"" + filepath.ToSlash(envPath) + "\"\n\n" + body
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeMinimalConfig(t *testing.T, extra string) string {
	t.Helper()
	return writeCatalogConfig(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[chat]
max_tokens = 8192
`+extra, "DEEPSEEK_API_KEY=test-key\n")
}

func TestModelCatalogIsProviderQualifiedAndStable(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "openrouter"

[providers.deepseek]
models = [
  { name = "deepseek/v4", context_window_tokens = 128000 },
]

[providers.openrouter]
models = [
  { name = "openai/gpt-4o-mini", context_window_tokens = 128000 },
  { name = "shared/model", context_window_tokens = 256000 },
]
default_model = "shared/model"

[providers.zai]
models = []

[chat]
max_tokens = 8192
`, "OPENROUTER_API_KEY=secret\nDEEPSEEK_API_KEY=deep\n")
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	groups := res.ModelCatalog()
	if len(groups) != 3 {
		t.Fatalf("catalog groups = %d, want 3", len(groups))
	}
	if got := []string{groups[0].Provider, groups[1].Provider, groups[2].Provider}; strings.Join(got, ",") != "openrouter,deepseek,zai" {
		t.Fatalf("provider order = %v", got)
	}
	if !groups[0].Active || groups[1].Active || groups[2].Selectable {
		t.Fatalf("group flags = %+v", groups)
	}
	if groups[2].DisabledReason == "" || strings.Contains(groups[2].DisabledReason, "secret") {
		t.Fatalf("unsafe/empty disabled reason = %q", groups[2].DisabledReason)
	}
	if got := groups[0].Models[1].Name; got != "shared/model" {
		t.Fatalf("slash model changed to %q", got)
	}
	copyOf := res.ModelCatalog()
	copyOf[0].Models[0].Name = "mutated"
	if got := res.ModelCatalog()[0].Models[0].Name; got == "mutated" {
		t.Fatal("catalog returned aliased model data")
	}
}

func TestModelCatalogRejectsMissingActiveModelsAndRegistryFallback(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "deepseek"

[providers.deepseek]
`, "DEEPSEEK_API_KEY=secret\n")
	_, err := Load(LoadOptions{ConfigPath: path})
	if err == nil || !strings.Contains(err.Error(), "models") {
		t.Fatalf("Load missing active catalog error = %v", err)
	}
}

func TestModelCatalogRejectsDuplicateNamesAndCaseCollidingProviders(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [
  { name = "same", context_window_tokens = 128000 },
  { name = "same", context_window_tokens = 128000 },
]

[providers.DEEPSEEK]
models = [
  { name = "other", context_window_tokens = 128000 },
]
`, "DEEPSEEK_API_KEY=secret\n")
	_, err := Load(LoadOptions{ConfigPath: path})
	if err == nil {
		t.Fatal("duplicate/case-colliding provider config was accepted")
	}
	if strings.Contains(err.Error(), "same") {
		t.Fatalf("duplicate error echoed model identifier: %q", err)
	}
}

func TestModelCatalogRejectsLegacyBudgetAndProviderKeys(t *testing.T) {
	tests := []struct {
		name  string
		extra string
		want  string
	}{
		{name: "legacy prompt key", extra: "max_context_tokens = 10000\n", want: "max_context_tokens"},
		{name: "invalid prompt cap", extra: "max_prompt_tokens = 0\n", want: "max_prompt_tokens"},
		{name: "legacy provider model", extra: "model = \"legacy\"\n", want: "model is no longer supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]
			`
			if tt.name == "legacy provider model" {
				body += tt.extra
			} else {
				body += "\n[chat]\n" + tt.extra + "max_tokens = 8192\n"
			}
			_, err := Load(LoadOptions{ConfigPath: writeCatalogConfig(t, body, "DEEPSEEK_API_KEY=test-key\n")})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestModelCatalogResolvesEffectivePromptBudget(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[chat]
max_tokens = 8192
max_prompt_tokens = 100000
`, "DEEPSEEK_API_KEY=test-key\n")
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if res.MaxContextTokens != 100000 {
		t.Fatalf("effective prompt budget = %d, want 100000", res.MaxContextTokens)
	}
}
