package provider

import (
	"context"
	"fmt"
	"net"
	"strings"

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
	} else if strings.TrimSpace(apiKey) == "" {
		// Cloud/non-loopback mode must be keyed. Failing closed here (mirroring
		// the NewForProvider gate) keeps the plan §12 invariant that a keyless
		// ollama client is constructible iff the base_url is a verified
		// loopback address: otherwise the exported constructor would return a
		// keyless client on the default (unpinned) transport, and keyless
		// traffic could leave the machine (round-9 confirmed finding).
		return nil, fmt.Errorf("missing API key for provider %q", "ollama")
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:              "ollama",
		BaseURL:           base,
		APIKey:            apiKey,
		CacheUsageEnabled: opts.CacheUsageEnabled,
		DialContext:       dialContext,
	}), nil
}
