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

	if usage.PromptTokens > 0 || usage.PromptCacheHitTokens != nil || usage.PromptTokensDetails != nil || usage.CompletionTokens != nil {
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
