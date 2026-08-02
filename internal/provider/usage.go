package provider

// TokenUsage holds provider-reported input and output token counts for one
// completion turn. Reported is false when the response carried no recognized
// usage fields — that means "not reported", not "zero tokens", and every
// other field is meaningless when it is false.
type TokenUsage struct {
	Reported     bool
	InputTokens  int
	OutputTokens int
}

// deriveTokenUsage converts a decoded usageWire into the normalized TokenUsage
// shape. A nil usage yields the zero value.
func deriveTokenUsage(usage *usageWire) TokenUsage {
	if usage == nil {
		return TokenUsage{}
	}
	reported := false
	inputTokens := 0
	outputTokens := 0

	// Cache-only fields (prompt_cache_hit_tokens / prompt_tokens_details) do
	// NOT mark token usage as reported: they describe cache reuse, not actual
	// token counts, and a response carrying only them would otherwise be
	// treated as a real zero-input observation and poison the calibration
	// ratio. A recognized completion count, a positive prompt count, or a
	// cache-miss field is what makes the token accounting real.
	if usage.PromptTokens > 0 || usage.PromptCacheMissTokens != nil || usage.CompletionTokens != nil {
		reported = true
	}
	inputTokens = nonNegative(usage.PromptTokens)
	if usage.CompletionTokens != nil {
		outputTokens = nonNegative(*usage.CompletionTokens)
	}

	if !reported {
		return TokenUsage{}
	}
	return TokenUsage{
		Reported:     true,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
}
