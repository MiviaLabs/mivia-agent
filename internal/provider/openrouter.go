package provider

import "github.com/MiviaLabs/mivia-agent/internal/config"

// NewOpenRouter returns an OpenRouter OpenAI-compatible completer.
func NewOpenRouter(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		base = config.OpenRouterDefaultURL
	}
	referer := opts.HTTPReferer
	if referer == "" {
		referer = "https://github.com/MiviaLabs/mivia-agent"
	}
	title := opts.XTitle
	if title == "" {
		title = "mivia"
	}
	return NewOpenAICompat(config.OpenRouterName, base, opts.APIKey, referer, title), nil
}
