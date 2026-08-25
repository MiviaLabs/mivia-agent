package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewOpenRouter returns an OpenRouter OpenAI-compatible completer.
func NewOpenRouter(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("openrouter")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "openrouter")
		}
		base = descriptor.DefaultURL
	}
	referer := opts.HTTPReferer
	if referer == "" {
		referer = "https://github.com/MiviaLabs/mivia-agent"
	}
	title := opts.XTitle
	if title == "" {
		title = "Mivia Agent"
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name: "openrouter", BaseURL: base, APIKey: opts.APIKey, DialContext: opts.DialContext,
		HTTPReferer: referer, XTitle: title, CacheUsageEnabled: opts.CacheUsageEnabled,
		// OpenRouter forwards Anthropic-style cache_control content markers to
		// upstream models with explicit caching and documents that models
		// without it ignore the marker, so the stable prefix is marked
		// whenever prompt_cache is not "off".
		CacheMarkersEnabled: opts.CacheMarkersEnabled,
		// OpenRouter accepts the top-level reasoning_effort shorthand on Chat
		// Completions and normalizes it per upstream model. A model that wants
		// the canonical nested object names reasoning_dialect = "openrouter".
		// The value comes from the vetted table config validates against; see
		// defaultReasoningDialect.
		Reasoning: defaultReasoningDialect("openrouter"),
		// Reasoning models on OpenRouter degrade in multi-turn tool loops when
		// prior thinking is not echoed; replay assistant reasoning under the
		// canonical wire field "reasoning" so the openrouter path captures and
		// replays it on tool-call turns.
		RequiresReasoningReplay: true,
		ReplayReasoningField:    "reasoning",
		// OpenRouter keys upstream routing stickiness on the OpenAI-compatible
		// "user" field: requests sharing the same value are more likely to land
		// on the same warm upstream provider connection. Only openrouter opts in
		// - other factories leave this false so their bodies stay unchanged.
		SendSessionUserKey: true,
	}), nil
}
