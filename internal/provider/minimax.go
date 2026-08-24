package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewMiniMax returns an OpenAI-compatible completer for the MiniMax API.
func NewMiniMax(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("minimax")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "minimax")
		}
		base = descriptor.DefaultURL
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:              "minimax",
		BaseURL:           base,
		APIKey:            opts.APIKey,
		CacheUsageEnabled: opts.CacheUsageEnabled,
	}), nil
}
