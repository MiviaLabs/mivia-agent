package provider

import (
	"context"
	"fmt"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewLLMProxyCLI returns a completer for a local LLM proxy (such as
// llmproxycli / LiteLLM / CLI proxy) running on a local loopback or custom
// endpoint. Every model on this provider speaks OpenAI-compatible
// chat/completions by default, EXCEPT any model entry that explicitly sets
// reasoning_dialect = "anthropic_adaptive" (opts.AnthropicNativeModels,
// computed by NewForProvider from the resolved catalog - see
// anthropicNativeModelsFor): that model's requests instead go out through a
// native Anthropic Messages API completer built against THIS provider's own
// BaseURL/APIKey, not a separate anthropic provider config. This exists
// because a proxy that translates OpenAI-compat requests to Anthropic's real
// API cannot always deliver Anthropic's own request-shape constraints (e.g.
// a non-default temperature rejected outright once reasoning is active) -
// speaking Anthropic's wire format directly to a proxy that also exposes it
// natively sidesteps the translation entirely.
func NewLLMProxyCLI(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("llmproxycli")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "llmproxycli")
		}
		base = descriptor.DefaultURL
	}
	dialect := opts.ReasoningDialect
	if dialect == "" {
		dialect = defaultReasoningDialect("llmproxycli")
	}
	compat := NewOpenAICompatWithOptions(CompatOptions{
		Name:                    "llmproxycli",
		BaseURL:                 base,
		APIKey:                  opts.APIKey,
		DialContext:             opts.DialContext,
		CacheUsageEnabled:       opts.CacheUsageEnabled,
		CacheMarkersEnabled:     opts.CacheMarkersEnabled,
		Reasoning:               dialect,
		RequiresReasoningReplay: true,
	})
	if len(opts.AnthropicNativeModels) == 0 {
		// No model on this provider opted in - identical to today's
		// behavior for every existing config, byte-for-byte.
		return compat, nil
	}
	native := newAnthropicCompleter("llmproxycli", base, opts.APIKey, opts.DialContext)
	nativeModels := make(map[string]bool, len(opts.AnthropicNativeModels))
	for _, name := range opts.AnthropicNativeModels {
		nativeModels[name] = true
	}
	return &llmProxyDispatchCompleter{
		name:         "llmproxycli",
		compat:       compat,
		native:       native,
		nativeModels: nativeModels,
	}, nil
}

// llmProxyDispatchCompleter routes each call to one of two inner completers
// by Request.Model, decided fresh per call rather than once at construction.
// This has to be per-call: a same-provider subagent delegation can reuse an
// already-constructed Completer while overriding only the model
// (internal/cliagents/agent_binding.go - "Completers are provider-scoped:
// adapters take the model from each request, not from construction"), so a
// single instance of this type can and does see requests for both an
// AnthropicNativeModels entry and an ordinary OpenAI-compat model across one
// session's lifetime.
//
// This type deliberately does NOT implement ReasoningPolicyAware or
// ContextAccountingAware. Both are optional per-Completer-instance
// capabilities read via type assertion (see ReasoningPolicyFor,
// ContextAccountingFor in provider.go); llmproxycli's CompatOptions above
// sets RequiresReasoningReplay but never RejectReasoningLessToolTurns or
// ContextAccounting, so the ONLY field either interface's sole real caller
// consumes (ReasoningPolicy().RejectReasoningLess, read once in
// internal/chat/turn_finish.go) is already false for llmproxycli today,
// compat-routed or not - a Completer that doesn't implement the interface at
// all resolves to the same zero-value default ReasoningPolicyFor/
// ContextAccountingFor already return in that case. If llmproxycli ever
// starts setting either of those CompatOptions fields, this type needs
// updating to forward them per the ACTIVE request's target, not just once.
type llmProxyDispatchCompleter struct {
	name         string
	compat       *OpenAICompat
	native       *AnthropicCompleter
	nativeModels map[string]bool
}

var _ Completer = (*llmProxyDispatchCompleter)(nil)

// Name implements Completer.
func (c *llmProxyDispatchCompleter) Name() string { return c.name }

// pick returns the completer that should handle a request for model - the
// native Anthropic completer for an opted-in model, the OpenAI-compat one
// for everything else (including the empty string: a caller that never sets
// Request.Model gets today's default behavior, not a guess).
func (c *llmProxyDispatchCompleter) pick(model string) Completer {
	if c.nativeModels[model] {
		return c.native
	}
	return c.compat
}

// ChatStream implements Completer.
func (c *llmProxyDispatchCompleter) ChatStream(ctx context.Context, req Request, w io.Writer) (string, error) {
	return c.pick(req.Model).ChatStream(ctx, req, w)
}

// Chat implements Completer.
func (c *llmProxyDispatchCompleter) Chat(ctx context.Context, req Request) (string, error) {
	return c.pick(req.Model).Chat(ctx, req)
}

// ChatTurn implements Completer.
func (c *llmProxyDispatchCompleter) ChatTurn(ctx context.Context, req Request) (*Response, error) {
	return c.pick(req.Model).ChatTurn(ctx, req)
}
