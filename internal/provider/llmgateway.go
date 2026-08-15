package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewLLMGateway returns an LLM Gateway OpenAI-compatible completer.
// One code path serves DevPass and pay-as-you-go keys: same endpoint, same
// Bearer auth, same error envelope; the model-ID difference (DevPass rejects
// provider-prefixed IDs with 403) is gateway-side enforcement.
func NewLLMGateway(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("llmgateway")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "llmgateway")
		}
		base = descriptor.DefaultURL
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:    "llmgateway",
		BaseURL: base,
		APIKey:  opts.APIKey,
		// LLM Gateway injects Anthropic cache_control markers itself using
		// per-model minimums and TTL ordering, and strips all client markers
		// when the project disables provider cache writes. mivia marking the
		// stable prefix would trigger the gateway's "skip automatic
		// injection" mixing rule and fight its TTL ordering, so markers stay
		// with the gateway. Cached-token usage capture still works.
		CacheUsageEnabled: opts.CacheUsageEnabled,
		// The OpenAI-compatible surface accepts the reasoning_effort
		// shorthand and normalizes it per upstream model. Vetted default
		// dialect "openai"; see defaultReasoningDialect.
		Reasoning: defaultReasoningDialect("llmgateway"),
		// LLM Gateway resolves a session key from (in priority order)
		// x-session-id, x-session-affinity, prompt_cache_key, or the
		// OpenAI-compatible "user" field; sessions pin provider routing for
		// prompt-cache locality and drive upstream prompt_cache_key
		// derivation. The hashed user field mivia already emits satisfies
		// this with no raw session id on the wire.
		SendSessionUserKey: true,
	}), nil
}
