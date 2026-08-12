package provider

import (
	"context"
	"fmt"
	"net"

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
	var dialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	if config.IsOllamaLoopback(base) {
		// Keyless local daemon mode. Resolve the loopback host once at
		// construction and pin every dial to the verified address, so a
		// resolver answering localhost with a non-loopback address fails
		// closed instead of receiving keyless plaintext (plan §12 item 1).
		apiKey = ""
		var err error
		dialContext, err = newLoopbackDialContext(base)
		if err != nil {
			return nil, err
		}
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:              "ollama",
		BaseURL:           base,
		APIKey:            apiKey,
		CacheUsageEnabled: opts.CacheUsageEnabled,
		DialContext:       dialContext,
	}), nil
}
