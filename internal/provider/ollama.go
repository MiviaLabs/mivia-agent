package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/config"
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
	apiKey := opts.APIKey
	if config.IsOllamaLoopback(base) {
		apiKey = ""
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:              "ollama",
		BaseURL:           base,
		APIKey:            apiKey,
		CacheUsageEnabled: opts.CacheUsageEnabled,
	}), nil
}
