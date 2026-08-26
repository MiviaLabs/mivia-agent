package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewLLMProxyCLI returns an OpenAI-compatible completer for a local LLM proxy
// (such as llmproxycli / LiteLLM / CLI proxy) running on a local loopback or custom endpoint.
func NewLLMProxyCLI(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("llmproxycli")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "llmproxycli")
		}
		base = descriptor.DefaultURL
	}
	dialect := opts.ReasoningDialect
	if dialect == "" {
		dialect = defaultReasoningDialect("llmproxycli")
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:                    "llmproxycli",
		BaseURL:                 base,
		APIKey:                  opts.APIKey,
		DialContext:             opts.DialContext,
		CacheUsageEnabled:       opts.CacheUsageEnabled,
		CacheMarkersEnabled:     opts.CacheMarkersEnabled,
		Reasoning:               dialect,
		RequiresReasoningReplay: true,
	}), nil
}
