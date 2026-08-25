package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewDeepSeek returns a DeepSeek OpenAI-compatible completer.
//
// DeepSeek thinking mode requires reasoning_content to be replayed on
// subsequent tool-call turns (RequiresReasoningReplay) and 400s on a tools
// request that includes a reasoning-less tool-call turn
// (RejectReasoningLessToolTurns). The default dialect is read from the vetted
// table (thinking_effort) so config validation and the client agree.
func NewDeepSeek(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("deepseek")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "deepseek")
		}
		base = descriptor.DefaultURL
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:                         "deepseek",
		BaseURL:                      base,
		APIKey:                       opts.APIKey,
		DialContext:                  opts.DialContext,
		CacheUsageEnabled:            opts.CacheUsageEnabled,
		RequiresReasoningReplay:      true,
		RejectReasoningLessToolTurns: true,
		Reasoning:                    defaultReasoningDialect("deepseek"),
		// DeepSeek's reasoning_model guide documents that reasoning_content
		// from a previous round is not sent back as billed input on later
		// requests - only the current, still-open tool exchange's reasoning
		// counts.
		ContextAccounting: ContextAccountingProfile{ReasoningBilling: ReasoningBillingTerminalExchange},
	}), nil
}
