package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewDeepSeek returns a DeepSeek OpenAI-compatible completer.
//
// No default reasoning dialect is set. DeepSeek's thinking mode expects
// reasoning_content to be replayed on subsequent tool-call turns, and
// provider.Message does not preserve that field, so defaulting a dialect here
// would break multi-step tool turns. A model entry may opt in by naming
// reasoning_dialect explicitly; that is the operator's informed choice.
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
