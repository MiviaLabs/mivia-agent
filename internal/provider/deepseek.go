package provider

import "github.com/MiviaLabs/mivia-agent/internal/providerregistry"

// NewDeepSeek returns a DeepSeek OpenAI-compatible completer.
func NewDeepSeek(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, _ := providerregistry.Lookup("deepseek")
		base = descriptor.DefaultURL
	}
	return NewOpenAICompatWithOptions(CompatOptions{Name: "deepseek", BaseURL: base, APIKey: opts.APIKey}), nil
}
