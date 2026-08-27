package config

import (
	"strings"
	"testing"
)

// go-toml dispatches ModelSpec.UnmarshalTOML for inline tables only, so an
// array-of-tables entry reaches the catalog carrying whatever strings the file
// held. Validation that fires for one TOML spelling is not validation.
func TestArrayOfTablesModelRejectsBadReasoningValues(t *testing.T) {
	cases := map[string]string{
		"bad level in set": `reasoning_efforts = ["banana"]`,
		"duplicate level":  `reasoning_efforts = ["high", "high"]`,
		"empty level":      `reasoning_efforts = ["high", ""]`,
		"bad default":      `reasoning_efforts = ["high"]` + "\n" + `reasoning = "banana"`,
		"bad dialect":      `reasoning_dialect = "wire-goes-brrr"`,
	}
	for name, keys := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[[providers.zai.models]]
name = "glm-5.2"
context_window_tokens = 1000000
`+keys+"\n")
			if err == nil {
				t.Fatalf("%s must be rejected in an array-of-tables entry too", name)
			}
		})
	}
}

// A valid array-of-tables entry must still load, or the re-validation would be
// rejecting the spelling rather than the values.
func TestArrayOfTablesModelAcceptsGoodReasoningValues(t *testing.T) {
	res, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[[providers.zai.models]]
name = "glm-5.2"
context_window_tokens = 1000000
reasoning_efforts = ["low", "high"]
reasoning = "high"
reasoning_dialect = "thinking_effort"
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec := profileNamed(t, res, "zai", "glm-5.2")
	if len(spec.ReasoningEfforts) != 2 {
		t.Fatalf("ReasoningEfforts = %v", spec.ReasoningEfforts)
	}
}

// A dotted key inside the closed model object names a nested table the shape
// does not have. Applying its first part would set a field the operator never
// wrote.
func TestDottedKeyInsideModelObjectIsRejected(t *testing.T) {
	cases := map[string]string{
		"dotted name":    `{ name.bogus = "x", context_window_tokens = 1000000 }`,
		"dotted efforts": `{ name = "glm-5.2", context_window_tokens = 1000000, reasoning_efforts.oops = ["low", "high"] }`,
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

// The thinking dialect has exactly two wire outputs, enabled and disabled. Two
// or more distinct graded levels under it would all produce a byte-identical
// body, so /effort would report success and change nothing.
func TestGradedEffortsRefusedOnAnUngradedDialect(t *testing.T) {
	_, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-5.2", context_window_tokens = 1000000, reasoning_efforts = ["low", "medium", "high", "max"], reasoning = "low" }]
`)
	if err == nil {
		t.Fatal("graded efforts on the thinking dialect must fail to load")
	}
	for _, want := range []string{"glm-5.2", "thinking", `reasoning_dialect = "thinking_effort"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q so the fix is obvious, got: %v", want, err)
		}
	}
}

// One graded level plus off is exactly what the thinking dialect expresses, so
// the check must not swallow the shape it was designed for.
func TestSingleGradedEffortIsAcceptedOnTheThinkingDialect(t *testing.T) {
	if _, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-4.6", context_window_tokens = 200000, reasoning_efforts = ["off", "high"], reasoning = "high" }]
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// The openrouter_onoff dialect is non-gradable (reasoning.enabled only), so it
// accepts exactly one graded level plus off - the shape poolside/laguna-s-2.1
// declares. This pins that a new dialect joins the closed set without breaking
// the deliverability check for its intended model entry.
func TestOpenRouterOnOffDialectAcceptsSingleGradedLevel(t *testing.T) {
	if _, err := loadReasoningCatalog(t, `[provider]
name = "openrouter"

[providers.openrouter]
models = [{ name = "poolside/laguna-s-2.1", context_window_tokens = 1048576, max_output_tokens = 131072, reasoning_efforts = ["off", "max"], reasoning = "max", reasoning_dialect = "openrouter_onoff" }]
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// anthropic_adaptive is Anthropic's native wire dialect - only a provider
// whose client can actually speak that shape may declare it (see
// reasoning.CanCarryDialect). deepseek is not in that allow-list: its client
// only ever speaks OpenAI-compatible chat/completions, so a model entry
// naming this dialect there would otherwise reach the wire as a malformed
// request rather than "declares a capability that sends nothing" (the
// failure mode the deliverability check exists to catch for every other
// dialect).
func TestAnthropicAdaptiveDialectRejectedOnANonCapableProvider(t *testing.T) {
	_, err := loadReasoningCatalog(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 1000000, reasoning_efforts = ["low", "high"], reasoning = "low", reasoning_dialect = "anthropic_adaptive" }]
`)
	if err == nil {
		t.Fatal("anthropic_adaptive on a non-capable provider must fail to load")
	}
	for _, want := range []string{"deepseek-v4-flash", "anthropic_adaptive", "deepseek"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q so the fix is obvious, got: %v", want, err)
		}
	}
}

// llmproxycli IS in the capability allow-list (its factory builds a
// per-model dispatcher to a native Anthropic completer for exactly this
// dialect - internal/provider/llmproxycli.go), so the same declaration that
// fails on deepseek above must succeed here.
func TestAnthropicAdaptiveDialectAcceptedOnLLMProxyCLI(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "llmproxycli"

[providers.llmproxycli]
models = [{ name = "claude-sonnet-5", context_window_tokens = 200000, reasoning_efforts = ["low", "medium", "high", "max"], reasoning = "high", reasoning_dialect = "anthropic_adaptive" }]

[chat]
max_tokens = 8192
`, "CLIPROXY_API_KEY=ck\n")
	if _, err := Load(LoadOptions{ConfigPath: path}); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// The graded set is deliverable once the dialect can express it.
func TestGradedEffortsAcceptedOnThinkingEffort(t *testing.T) {
	if _, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-5.2", context_window_tokens = 1000000, reasoning_efforts = ["low", "high"], reasoning = "low", reasoning_dialect = "thinking_effort" }]
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// ModelCatalog promises a deep copy, and the reasoning effort slice is the one
// reference field a ModelSpec carries.
func TestModelCatalogCopiesReasoningEfforts(t *testing.T) {
	res, err := loadReasoningCatalog(t, `[provider]
name = "zai"

[providers.zai]
models = [{ name = "glm-5.2", context_window_tokens = 1000000, reasoning_efforts = ["low", "high"], reasoning = "low", reasoning_dialect = "thinking_effort" }]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	first := profileNamed(t, res, "zai", "glm-5.2")
	first.ReasoningEfforts[0] = "tampered"
	if got := profileNamed(t, res, "zai", "glm-5.2").ReasoningEfforts[0]; got != "low" {
		t.Fatalf("a caller's write reached the stored catalog: %q", got)
	}
}
