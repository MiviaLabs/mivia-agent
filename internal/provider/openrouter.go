package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// NewOpenRouter returns an OpenRouter OpenAI-compatible completer.
func NewOpenRouter(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("openrouter")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "openrouter")
		}
		base = descriptor.DefaultURL
	}
	referer := opts.HTTPReferer
	if referer == "" {
		referer = "https://mivia.app"
	}
	title := opts.XTitle
	if title == "" {
		title = "mivia.app"
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name: "openrouter", BaseURL: base, APIKey: opts.APIKey,
		HTTPReferer: referer, XTitle: title, CacheUsageEnabled: opts.CacheUsageEnabled,
		// OpenRouter accepts the top-level reasoning_effort shorthand on Chat
		// Completions and normalizes it per upstream model. A model that wants
		// the canonical nested object names reasoning_dialect = "openrouter".
		Reasoning: reasoning.DialectOpenAI,
	}), nil
}
