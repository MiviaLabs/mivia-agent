package provider

import "github.com/MiviaLabs/mivia-agent/internal/providerregistry"

// NewOpenRouter returns an OpenRouter OpenAI-compatible completer.
func NewOpenRouter(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, _ := providerregistry.Lookup("openrouter")
		base = descriptor.DefaultURL
	}
	referer := opts.HTTPReferer
	if referer == "" {
		referer = "https://github.com/MiviaLabs/mivia-agent"
	}
	title := opts.XTitle
	if title == "" {
		title = "mivia"
	}
	return NewOpenAICompatWithOptions(CompatOptions{Name: "openrouter", BaseURL: base, APIKey: opts.APIKey, HTTPReferer: referer, XTitle: title}), nil
}
