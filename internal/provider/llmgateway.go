package provider

import (
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewLLMGateway returns an LLM Gateway OpenAI-compatible completer.
// One code path serves DevPass and pay-as-you-go keys: same endpoint, same
// Bearer auth, same error envelope; the model-ID difference (DevPass rejects
// provider-prefixed IDs with 403) is gateway-side enforcement.
func NewLLMGateway(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		// "llmgateway" is a compile-time-registered descriptor key
		// (providerregistry/registry.go), so Lookup here always succeeds.
		descriptor, _ := providerregistry.Lookup("llmgateway")
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
		// dialect "openai"; see defaultReasoningDialect. A model entry whose
		// upstream is itself a thinking-mode model (e.g. a DeepSeek route)
		// overrides this per-model via [providers.llmgateway].models[].reasoning_dialect.
		Reasoning: defaultReasoningDialect("llmgateway"),
		// llmgateway fronts a heterogeneous, caller-chosen set of upstream
		// models - unlike the single-vendor "deepseek" factory, it cannot
		// declare RejectReasoningLessToolTurns (DeepSeek's documented-400
		// gate): that gate DROPS older tool-call turns lacking
		// reasoning_content, which is the correct repair only for DeepSeek
		// and would silently corrupt tool-call history for a non-thinking
		// model routed through the same gateway.
		//
		// RequiresReasoningReplay is safe to enable unconditionally, though:
		// toAPIMessages only ever copies reasoning_content onto the wire for
		// an assistant message that already carries it (see api_message.go),
		// so a model that never populates the field sees no change to its
		// request body. For a DeepSeek-family model behind the gateway, this
		// closes the gap where mivia dropped the model's own prior
		// chain-of-thought on every tool-call turn - replaying it verbatim,
		// like the native "deepseek" provider does, so consecutive requests
		// reconstruct the same prefix the upstream's own prompt cache
		// indexed instead of a structurally different one on every turn.
		RequiresReasoningReplay: true,
		// LLM Gateway resolves a session key from (in priority order)
		// x-session-id, x-session-affinity, prompt_cache_key, or the
		// OpenAI-compatible "user" field; sessions pin provider routing for
		// prompt-cache locality and drive upstream prompt_cache_key
		// derivation. The hashed user field mivia already emits satisfies
		// this with no raw session id on the wire.
		SendSessionUserKey: true,
	}), nil
}
