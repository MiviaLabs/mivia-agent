package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewOllama returns an Ollama OpenAI-compatible completer.
func NewOllama(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("ollama")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "ollama")
		}
		base = descriptor.DefaultURL
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:              "ollama",
		BaseURL:           base,
		APIKey:            opts.APIKey,
		CacheUsageEnabled: opts.CacheUsageEnabled,
	}), nil
}
