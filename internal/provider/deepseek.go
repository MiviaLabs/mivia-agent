package provider

import "github.com/MiviaLabs/mivia-agent/internal/config"

// NewDeepSeek returns a DeepSeek OpenAI-compatible completer.
func NewDeepSeek(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		base = config.DeepSeekDefaultURL
	}
	return NewOpenAICompat(config.DeepSeekName, base, opts.APIKey, "", ""), nil
}
