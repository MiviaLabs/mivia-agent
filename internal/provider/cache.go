package provider

// CacheStyle describes how a provider's wire format expresses prompt-cache
// reuse. Implicit means the provider caches automatically based on
// request-prefix bytes with no request-side marker - the only style any
// provider in this codebase speaks today. Explicit (marker-style, e.g.
// Anthropic cache_control content blocks) is reserved for a future provider
// this codebase does not yet configure or test against.
type CacheStyle string

const (
	CacheStyleNone     CacheStyle = "none"
	CacheStyleImplicit CacheStyle = "implicit"
	CacheStyleExplicit CacheStyle = "explicit"
)

// CacheUsage is provider-reported prompt-cache accounting for one turn.
// Reported is false when the response carried none of the recognized cache
// usage fields - that means "not reported", not "zero tokens cached", and
// every other field is meaningless when it is false. Token counts decoded
// from an untrusted upstream response are clamped to zero rather than
// propagated as negative accounting.
type CacheUsage struct {
	Reported          bool
	Style             CacheStyle
	InputTokens       int
	CachedInputTokens int
	CacheWriteTokens  int
}

// usageWire is the tolerant decode shape for a provider's `usage` object.
// Pointer fields distinguish "field absent" from "field present as zero" -
// the same pattern apiMessage.Content and streamToolCallDelta.Index already
// use in this package for exactly this reason.
//
// Two conventions are recognized:
//   - DeepSeek: flat prompt_cache_hit_tokens / prompt_cache_miss_tokens.
//   - OpenAI/OpenRouter: nested prompt_tokens_details.{cached_tokens,cache_write_tokens}.
type usageWire struct {
	PromptTokens          int                      `json:"prompt_tokens"`
	PromptCacheHitTokens  *int                     `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens *int                     `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails   *promptTokensDetailsWire `json:"prompt_tokens_details"`
	CompletionTokens      *int                     `json:"completion_tokens"`
}

type promptTokensDetailsWire struct {
	CachedTokens     *int `json:"cached_tokens"`
	CacheWriteTokens *int `json:"cache_write_tokens"`
}

// deriveCacheUsage converts a decoded usage object into the normalized
// CacheUsage shape. A nil usage (the field was absent from the response)
// yields the zero value.
//
// When both conventions report a cached-token count and disagree, the flat
// DeepSeek-style field wins: it is the more specific convention, documented
// for exactly this purpose, while the nested shape is a general accounting
// envelope a proxy may forward unchanged. CacheWriteTokens has no flat
// equivalent and always comes from the nested shape when present.
func deriveCacheUsage(usage *usageWire, style CacheStyle) CacheUsage {
	if usage == nil {
		return CacheUsage{}
	}
	reported := false
	cached, write := 0, 0

	switch {
	case usage.PromptCacheHitTokens != nil:
		cached = nonNegative(*usage.PromptCacheHitTokens)
		reported = true
	case usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens != nil:
		cached = nonNegative(*usage.PromptTokensDetails.CachedTokens)
		reported = true
	}
	if usage.PromptCacheMissTokens != nil {
		reported = true
	}
	if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CacheWriteTokens != nil {
		write = nonNegative(*usage.PromptTokensDetails.CacheWriteTokens)
		reported = true
	}

	if !reported {
		return CacheUsage{}
	}
	return CacheUsage{
		Reported: true, Style: style,
		InputTokens:       nonNegative(usage.PromptTokens),
		CachedInputTokens: cached,
		CacheWriteTokens:  write,
	}
}

func nonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
