package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func TestModelSpecDecodesDeclaredEfforts(t *testing.T) {
	res, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [
  { name = "glm-5.2", context_window_tokens = 1000000, reasoning_efforts = ["low", "medium", "high", "max"], reasoning = "high", reasoning_dialect = "thinking_effort" },
  { name = "glm-4.6", context_window_tokens = 200000 },
]
default_model = "glm-5.2"
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	configured := profileNamed(t, res, "zai", "glm-5.2")
	// Order is preserved because it is the order the /effort dialog lists.
	want := []reasoning.Level{reasoning.Low, reasoning.Medium, reasoning.High, reasoning.Max}
	if !slices.Equal(configured.ReasoningEfforts, want) {
		t.Fatalf("ReasoningEfforts = %v, want %v", configured.ReasoningEfforts, want)
	}
	if configured.Reasoning != reasoning.High {
		t.Fatalf("default = %q, want high", configured.Reasoning)
	}
	unset := profileNamed(t, res, "zai", "glm-4.6")
	if len(unset.ReasoningEfforts) != 0 {
		t.Fatalf("a model declaring nothing carried %v", unset.ReasoningEfforts)
	}
}

// A model may offer reasoning while shipping with it off: the user opts in
// through /effort. That is why an empty default with a non-empty set is legal.
func TestDeclaredEffortsWithoutADefaultAreLegal(t *testing.T) {
	res, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-5.2", context_window_tokens = 1000000, reasoning_efforts = ["low", "high"], reasoning_dialect = "thinking_effort" }]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec := profileNamed(t, res, "zai", "glm-5.2")
	if spec.Reasoning.Active() {
		t.Fatalf("default = %q, want unset", spec.Reasoning)
	}
	if len(spec.ReasoningEfforts) != 2 {
		t.Fatalf("efforts = %v", spec.ReasoningEfforts)
	}
}

// The set is the source of truth and the default a pointer into it. A default
// outside the set would be a value /effort could never return to.
func TestDefaultEffortMustBeAmongTheDeclaredEfforts(t *testing.T) {
	_, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-5.2", context_window_tokens = 1000000, reasoning_efforts = ["low", "high"], reasoning = "max", reasoning_dialect = "thinking_effort" }]
`)
	if err == nil {
		t.Fatal("a default outside the declared set must fail to load")
	}
	for _, want := range []string{"glm-5.2", "max", `"low", "high"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q, got: %v", want, err)
		}
	}
}

func TestDefaultEffortWithoutADeclaredSetIsRejected(t *testing.T) {
	_, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-5.2", context_window_tokens = 1000000, reasoning = "high" }]
`)
	if err == nil {
		t.Fatal("a default with no declared set must fail to load")
	}
	if !strings.Contains(err.Error(), "reasoning_efforts") {
		t.Fatalf("error must name the missing key, got: %v", err)
	}
}

// The dialect check now keys on the CAPABILITY, not the default: /effort can
// activate any listed level, so a set with no resolvable dialect is unusable
// even when the model ships with reasoning off.
func TestDeclaredEffortsRequireAResolvableDialect(t *testing.T) {
	_, err := loadReasoningCatalog(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 1000000, reasoning_efforts = ["high"] }]
`)
	if err == nil {
		t.Fatal("declared efforts with no resolvable dialect must fail to load")
	}
	if !strings.Contains(err.Error(), "reasoning_dialect") {
		t.Fatalf("error must name the key to add, got: %v", err)
	}
}

func TestInvalidDeclaredEffortsAreRejected(t *testing.T) {
	cases := map[string]string{
		"unknown level":   `reasoning_efforts = ["turbo"]`,
		"duplicate":       `reasoning_efforts = ["high", "high"]`,
		"not an array":    `reasoning_efforts = "high"`,
		"non-string item": `reasoning_efforts = [3]`,
		"empty item":      `reasoning_efforts = [""]`,
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-5.2", context_window_tokens = 1000000, `+entry+` }]
`); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

// An explicitly empty array is the same statement as omitting the key: this
// model offers nothing. It must not trip the dialect requirement.
func TestEmptyDeclaredEffortsMeanNoReasoningSurface(t *testing.T) {
	res, err := loadReasoningCatalog(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 1000000, reasoning_efforts = [] }]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec := profileNamed(t, res, "deepseek", "deepseek-v4-flash")
	if len(spec.ReasoningEfforts) != 0 || spec.Reasoning.Active() {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestModelOffersReasoning(t *testing.T) {
	if ModelOffersReasoning(ModelSpec{}) {
		t.Fatal("a bare model offers nothing")
	}
	if !ModelOffersReasoning(ModelSpec{ReasoningEfforts: []reasoning.Level{reasoning.High}}) {
		t.Fatal("a declared set is an offer")
	}
}
