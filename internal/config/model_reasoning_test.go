package config

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func loadReasoningCatalog(t *testing.T, body string) (*Resolved, error) {
	t.Helper()
	path := writeCatalogConfig(t, body+"\n[chat]\nmax_tokens = 8192\n",
		"ZAI_API_KEY=zk\nDEEPSEEK_API_KEY=dk\nOPENROUTER_API_KEY=ok\n")
	return Load(LoadOptions{ConfigPath: path})
}

func profileNamed(t *testing.T, res *Resolved, provider, model string) ModelSpec {
	t.Helper()
	for _, group := range res.ModelCatalog() {
		if group.Provider != provider {
			continue
		}
		for _, spec := range group.Models {
			if spec.Name == model {
				return spec
			}
		}
	}
	t.Fatalf("model %s/%s not found in catalog", provider, model)
	return ModelSpec{}
}

func TestModelSpecDecodesReasoningKeys(t *testing.T) {
	res, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [
  { name = "glm-5.2", context_window_tokens = 1000000, reasoning = "high", reasoning_dialect = "thinking_effort" },
  { name = "glm-5-turbo", context_window_tokens = 200000 },
]
default_model = "glm-5.2"
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	configured := profileNamed(t, res, "zai", "glm-5.2")
	if configured.Reasoning != reasoning.High {
		t.Fatalf("Reasoning = %q, want high", configured.Reasoning)
	}
	if configured.ReasoningDialect != reasoning.DialectThinkingEffort {
		t.Fatalf("ReasoningDialect = %q, want thinking_effort", configured.ReasoningDialect)
	}
	// A model that says nothing must stay at the zero values, or it would
	// start sending a reasoning field it never asked for.
	unset := profileNamed(t, res, "zai", "glm-5-turbo")
	if unset.Reasoning != "" || unset.ReasoningDialect != "" {
		t.Fatalf("unconfigured model carried %q/%q", unset.Reasoning, unset.ReasoningDialect)
	}
}

// A provider with a vetted default needs no dialect key at all.
func TestReasoningWithoutDialectLoadsOnAVettedProvider(t *testing.T) {
	res, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-4.6", context_window_tokens = 200000, reasoning = "off" }]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec := profileNamed(t, res, "zai", "glm-4.6")
	if spec.Reasoning != reasoning.Off {
		t.Fatalf("Reasoning = %q, want off", spec.Reasoning)
	}
	if spec.ReasoningDialect != "" {
		t.Fatalf("ReasoningDialect = %q, want unset so the provider default applies", spec.ReasoningDialect)
	}
}

// The whole point of the load-time check: an active level on a provider with
// no vetted default would reach the client, resolve to no dialect, and send
// nothing. Silently doing nothing is worse than refusing.
func TestActiveReasoningRefusedWithoutAResolvableDialect(t *testing.T) {
	_, err := loadReasoningCatalog(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 1000000, reasoning = "high" }]
`)
	if err == nil {
		t.Fatal("an active level with no resolvable dialect must fail to load")
	}
	for _, want := range []string{"deepseek-v4-pro", "reasoning_dialect"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q so the fix is obvious, got: %v", want, err)
		}
	}
}

func TestExplicitDialectUnblocksAProviderWithoutADefault(t *testing.T) {
	res, err := loadReasoningCatalog(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 1000000, reasoning = "high", reasoning_dialect = "thinking_effort" }]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec := profileNamed(t, res, "deepseek", "deepseek-v4-pro")
	if spec.ReasoningDialect != reasoning.DialectThinkingEffort {
		t.Fatalf("ReasoningDialect = %q", spec.ReasoningDialect)
	}
}

// A dialect on its own declares capability for a model dialled off. It sends
// nothing, so it needs no provider default to be meaningful.
func TestDialectWithoutLevelIsAccepted(t *testing.T) {
	res, err := loadReasoningCatalog(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 1000000, reasoning_dialect = "thinking_effort" }]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec := profileNamed(t, res, "deepseek", "deepseek-v4-pro")
	if spec.Reasoning.Active() {
		t.Fatalf("Reasoning = %q, want unset", spec.Reasoning)
	}
}

// An explicit "none" dialect is a statement, not a missing key, so it does not
// satisfy an active level: the pair would still send nothing.
func TestActiveLevelWithExplicitNoneDialectIsRefused(t *testing.T) {
	_, err := loadReasoningCatalog(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 1000000, reasoning = "high", reasoning_dialect = "none" }]
`)
	if err == nil {
		t.Fatal("an active level on the none dialect must fail to load")
	}
}

func TestInvalidReasoningValuesAreRejected(t *testing.T) {
	cases := map[string]string{
		"bad level":        `{ name = "glm-4.6", context_window_tokens = 200000, reasoning = "turbo" }`,
		"bad dialect":      `{ name = "glm-4.6", context_window_tokens = 200000, reasoning_dialect = "qwen" }`,
		"level not string": `{ name = "glm-4.6", context_window_tokens = 200000, reasoning = 3 }`,
		"unknown key":      `{ name = "glm-4.6", context_window_tokens = 200000, reasoning_efort = "high" }`,
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [`+entry+`]
`); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}
