// Package provider - SDK capability mirror for AnthropicCompleter.
//
// AnthropicCompleter implements provider.ContextAccountant,
// provider.ReasoningPolicy, and provider.TokenEstimator from
// mivia-ai-sdk/provider directly, mirroring the SDK's own
// provider/anthropic/client.go and estimate.go. Today only
// agentLoopCompleter (internal/agent/agentloop_adoption.go) exposes
// these capabilities to the SDK loop, by wrapping whichever CLI
// completer is configured; this file is a proof-of-adoption showing
// one concrete provider can carry the capabilities itself. The other
// eight builtin providers (openai_compat.go and its per-vendor
// wrappers) are deliberately left as follow-up - see the Pass 7
// review report.
package provider

import (
	"encoding/json"

	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// Compile-time assertions: AnthropicCompleter satisfies all three
// optional SDK Completer capabilities.
var (
	_ sdkshape.ContextAccountant = (*AnthropicCompleter)(nil)
	_ sdkshape.ReasoningPolicy   = (*AnthropicCompleter)(nil)
	_ sdkshape.TokenEstimator    = (*AnthropicCompleter)(nil)
)

// ContextWindow implements sdkshape.ContextAccountant. It reports the
// context window NewAnthropic was configured with
// (Options.ContextWindowTokens, the resolved model's declared
// capacity), or 0 when the caller never supplied one - the SDK
// treats 0 the same way ContextWindow's doc describes an unknown
// window: no default Window derivation.
func (c *AnthropicCompleter) ContextWindow() int {
	return c.contextWindow
}

// ReasoningEffort implements sdkshape.ReasoningPolicy. Unlike the
// SDK's own provider/anthropic/client.go, AnthropicCompleter carries
// no completer-level default reasoning effort: the dial rides each
// Request (Request.ReasoningLevel, resolved per call by
// buildRequestBody), not the completer's construction-time
// configuration - see anthropic.go's package doc and provider.go's
// Options.ReasoningLevel comment. ReasoningEffort reports the empty
// string, matching agentLoopCompleter's own "no default" contract for
// a level with no SDK equivalent, so the SDK request stays exactly as
// empty as the legacy path sends it.
func (c *AnthropicCompleter) ReasoningEffort() string {
	return ""
}

// EstimateTokens implements sdkshape.TokenEstimator. It converts the
// SDK's Request shape to this package's own Message/ToolSpec shape
// and reuses EstimatePromptCost, so an SDK-side compaction trigger
// uses the same token semantics context.go already calibrated for
// this provider. The conversion here is lighter than
// internal/agent/agentloop_convert.go's sdkMessagesToCLI (role,
// content, name, tool-call-ID, and reasoning text only - no tool-call
// argument round trip): EstimatePromptCost only ever reads those
// fields, and internal/provider cannot import internal/agent (that
// import runs the other way), so this is an intentionally narrower,
// local converter rather than a shared one.
func (c *AnthropicCompleter) EstimateTokens(req sdkshape.Request) (int, error) {
	return EstimatePromptCost(sdkRequestMessagesToProvider(req.Messages), sdkToolDefinitionsToProvider(req.Tools), defaultContextAccountingProfile())
}

// sdkRequestMessagesToProvider converts SDK messages to this
// package's Message shape for EstimatePromptCost. See EstimateTokens
// for what is intentionally left out.
func sdkRequestMessagesToProvider(msgs []sdkshape.Message) []Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		var reasoningText string
		for _, b := range m.ReasoningBlocks {
			reasoningText += b.Content
		}
		out[i] = Message{
			Role:             string(m.Role),
			Content:          m.Content,
			ToolCallID:       m.ToolCallID,
			Name:             m.Name,
			ReasoningContent: reasoningText,
		}
	}
	return out
}

// sdkToolDefinitionsToProvider converts SDK tool definitions to this
// package's ToolSpec shape (the OpenAI-style function-wrapper map
// EstimateRequestCost expects), mirroring
// internal/agent/agentloop_convert.go's sdkToolDefsToCLI.
func sdkToolDefinitionsToProvider(defs []sdkshape.ToolDefinition) []ToolSpec {
	if len(defs) == 0 {
		return nil
	}
	out := make([]ToolSpec, 0, len(defs))
	for _, d := range defs {
		var params any = map[string]any{}
		if len(d.Schema) > 0 {
			params = json.RawMessage(d.Schema)
		}
		out = append(out, ToolSpec{
			"type": "function",
			"function": map[string]any{
				"name":        d.Name,
				"description": d.Description,
				"parameters":  params,
			},
		})
	}
	return out
}

// defaultContextAccountingProfile returns the zero-value
// ContextAccountingProfile. AnthropicCompleter has no per-instance
// accounting profile the way internal/agent's Loop does (ctxProfile
// there comes from session config); EstimateTokens uses the package's
// own default heuristics, same as calling EstimatePromptCost with an
// unset profile anywhere else in this package.
func defaultContextAccountingProfile() ContextAccountingProfile {
	return ContextAccountingProfile{}
}
