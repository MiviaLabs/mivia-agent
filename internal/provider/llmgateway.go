package provider

import (
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
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
	dialect := opts.ReasoningDialect
	if dialect == "" {
		dialect = defaultReasoningDialect("llmgateway")
	}
	// llmgateway fronts a heterogeneous, caller-chosen set of upstream
	// models - unlike a single-vendor factory (deepseek, zai), it cannot
	// hard-code one wire dialect or one reasoning-replay contract. Instead it
	// reads what config already resolved for the SPECIFIC model this client
	// was built for (opts.ReasoningDialect, set by NewForProvider from
	// [providers.llmgateway].models[].reasoning_dialect - see
	// reasoningDialectFor in provider.go). Only a model explicitly declared
	// thinking_effort (DeepSeek's own wire dialect, e.g.
	// runware/deepseek-v4-flash) opts into DeepSeek's documented tool-turn
	// contract below; any other model, present or future, keeps the plain
	// default behavior.
	thinkingMode := dialect == reasoning.DialectThinkingEffort
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:         "llmgateway",
		BaseURL:      base,
		APIKey:       opts.APIKey,
		DialContext:  opts.DialContext,
		ExtraHeaders: llmgatewayExtraHeaders(thinkingMode),
		// LLM Gateway injects Anthropic cache_control markers itself using
		// per-model minimums and TTL ordering, and strips all client markers
		// when the project disables provider cache writes. mivia marking the
		// stable prefix would trigger the gateway's "skip automatic
		// injection" mixing rule and fight its TTL ordering, so markers stay
		// with the gateway. Cached-token usage capture still works.
		CacheUsageEnabled: opts.CacheUsageEnabled,
		// The OpenAI-compatible surface accepts the reasoning_effort
		// shorthand and normalizes it per upstream model. Falls back to the
		// vetted provider default ("openai"; see defaultReasoningDialect)
		// when the resolved model declares no reasoning_dialect override.
		Reasoning: dialect,
		// RequiresReasoningReplay is safe to enable unconditionally:
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
		// RejectReasoningLessToolTurns (DeepSeek's documented-400 gate) DROPS
		// older tool-call turns lacking reasoning_content - the correct
		// repair only for a DeepSeek-dialect model, and one that would
		// silently corrupt tool-call history for any other model sharing
		// this gateway client. Gating it on the resolved dialect, instead of
		// a hard-coded provider-wide flag, is what keeps this safe for a
		// model set the caller can extend at any time via mivia.toml.
		RejectReasoningLessToolTurns: thinkingMode,
		// LLM Gateway resolves a session key from (in priority order)
		// x-session-id, x-session-affinity, prompt_cache_key, or the
		// OpenAI-compatible "user" field; sessions pin provider routing for
		// prompt-cache locality and drive upstream prompt_cache_key
		// derivation. The hashed user field mivia already emits satisfies
		// this with no raw session id on the wire.
		SendSessionUserKey: true,
	}), nil
}

// llmgatewayExtraHeaders returns the extra headers for a thinking-mode
// (thinking_effort dialect) client, or nil for every other model.
//
// A provider-prefixed model id (e.g. runware/deepseek-v4-flash) still
// auto-fails-over to a DIFFERENT upstream if the pinned provider's uptime
// drops below the gateway's threshold (LLM Gateway routing docs). For a
// thinking_effort model that is a correctness risk, not just a
// cache-locality one: RejectReasoningLessToolTurns (set alongside this in
// NewLLMGateway) was validated against THIS provider's reasoning_content
// handling, and a silent mid-session jump to an unverified host could
// reintroduce the exact 400-loop that gate exists to prevent, with nothing
// in mivia able to tell the two apart from an ordinary cache miss.
// X-No-Fallback trades that away for a request that fails outright on an
// outage instead - recoverable through mivia's own retry/step-error path,
// unlike a silent behavior change. Left off for every other model: there, a
// failover only costs cache locality, and losing gateway resilience for
// that is not worth it.
func llmgatewayExtraHeaders(thinkingMode bool) map[string]string {
	if !thinkingMode {
		return nil
	}
	return map[string]string{"X-No-Fallback": "true"}
}
