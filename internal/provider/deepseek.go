package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewDeepSeek returns a DeepSeek OpenAI-compatible completer.
func NewDeepSeek(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("deepseek")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "deepseek")
		}
		base = descriptor.DefaultURL
	}
	return NewOpenAICompatWithOptions(CompatOptions{Name: "deepseek", BaseURL: base, APIKey: opts.APIKey, CacheUsageEnabled: opts.CacheUsageEnabled}), nil
}
