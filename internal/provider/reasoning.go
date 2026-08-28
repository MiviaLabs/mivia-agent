package provider

import (
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// reasoningBodyFields maps one provider-neutral level onto the wire fields a
// dialect expects. A nil result means "send nothing", which is the shape every
// request had before reasoning existed.
//
// There is deliberately no sampling policy here. The hypothesis that reasoning
// models reject temperature/top_p was disproved against current provider
// documentation - DeepSeek accepts and ignores sampling settings in thinking
// mode, Z.AI's own active-thinking example sends temperature, and OpenRouter
// advertises sampling support per model - so removing a field the provider
// accepts would change valid requests to avoid a 400 that does not occur.
// This function only ever ADDS keys.
//
// clear_thinking:false is emitted only for DialectThinkingPreserved (model
// opt-in). DeepSeek's thinking_effort and the plain thinking dialect never
// receive that field.
func reasoningBodyFields(dialect reasoning.Dialect, level reasoning.Level) map[string]any {
	if !level.Active() {
		return nil
	}
	switch dialect {
	case reasoning.DialectOpenAI:
		if level == reasoning.Off {
			return map[string]any{"reasoning_effort": "none"}
		}
		return map[string]any{"reasoning_effort": string(level)}
	case reasoning.DialectOpenRouter:
		if level == reasoning.Off {
			return map[string]any{"reasoning": map[string]any{"enabled": false}}
		}
		return map[string]any{"reasoning": map[string]any{"effort": string(level)}}
	case reasoning.DialectOpenRouterOnOff:
		// On/off-only OpenRouter models (no reasoning_effort surface, e.g.
		// poolside/laguna-s-2.1): every active level is thinking on, every
		// off is thinking off. Sending enabled alone avoids an effort value
		// the endpoint does not support.
		return map[string]any{"reasoning": map[string]any{"enabled": level != reasoning.Off}}
	case reasoning.DialectThinking:
		return map[string]any{"thinking": thinkingObject(level, false)}
	case reasoning.DialectThinkingEffort:
		if level == reasoning.Off {
			// The thinking object alone disables. Pairing it with an effort
			// value would put two contradictory instructions in one body.
			return map[string]any{"thinking": thinkingObject(level, false)}
		}
		return map[string]any{
			"thinking":         thinkingObject(level, false),
			"reasoning_effort": string(level),
		}
	case reasoning.DialectThinkingPreserved:
		// z.ai Preserved Thinking: clear_thinking:false when enabled, plus
		// reasoning_effort for graded depth (same shape as thinking_effort so
		// multi-level model entries remain deliverable).
		if level == reasoning.Off {
			return map[string]any{"thinking": thinkingObject(level, true)}
		}
		return map[string]any{
			"thinking":         thinkingObject(level, true),
			"reasoning_effort": string(level),
		}
	case reasoning.DialectAnthropicAdaptive:
		// Anthropic's native shape: a top-level thinking object whose "on"
		// type is "adaptive" (not "enabled" - thinkingObject's shape does not
		// apply here), paired with output_config.effort carrying the graded
		// level. Off sends thinking:disabled alone; effort is never sent
		// without an active level, matching every other graded dialect above.
		if level == reasoning.Off {
			return map[string]any{"thinking": map[string]any{"type": "disabled"}}
		}
		// display: "summarized" is required to get readable thinking text
		// back at all - Anthropic's default ("omitted") streams thinking
		// blocks with an empty text field. Without this, mivia's reasoning
		// panel (internal/ui/component/transcript) has nothing to show
		// regardless of how ReasoningContent is populated downstream.
		return map[string]any{
			"thinking":      map[string]any{"type": "adaptive", "display": "summarized"},
			"output_config": map[string]any{"effort": anthropicEffortForLevel(level)},
		}
	default:
		// reasoning.DialectNone, and any dialect this client does not know:
		// fail closed rather than guess a wire shape.
		return nil
	}
}

// anthropicEffortForLevel maps a provider-neutral Level onto Anthropic's
// output_config.effort vocabulary (low, medium, high, xhigh, max - no
// "minimal" or "off": Minimal folds onto the same "low" tier as Low, since
// Anthropic's effort ladder has no finer step below it, and Off never reaches
// this function (reasoningBodyFields returns before calling it for Off).
func anthropicEffortForLevel(level reasoning.Level) string {
	switch level {
	case reasoning.Minimal, reasoning.Low:
		return "low"
	case reasoning.Medium:
		return "medium"
	case reasoning.High:
		return "high"
	case reasoning.XHigh:
		return "xhigh"
	case reasoning.Max:
		return "max"
	default:
		return ""
	}
}

func thinkingObject(level reasoning.Level, preserved bool) map[string]any {
	if level == reasoning.Off {
		return map[string]any{"type": "disabled"}
	}
	if preserved {
		return map[string]any{"type": "enabled", "clear_thinking": false}
	}
	return map[string]any{"type": "enabled"}
}

// defaultReasoningDialect is how a provider factory states its wire dialect:
// by reading the vetted table in internal/reasoning that config validates
// model entries against. A provider absent from that table gets the empty
// dialect, so only a request naming its own shape sends anything.
func defaultReasoningDialect(provider string) reasoning.Dialect {
	dialect, _ := reasoning.DefaultDialect(provider)
	return dialect
}

// reasoningMaxTokensFloor is the conservative per-effort-level max_tokens
// stand-in used whenever a request leaves MaxTokens unset. It is shared by
// every client (Anthropic's native completer and the OpenAI-compatible
// clients) rather than left to each provider's own server-side default:
// omitting max_tokens on an OpenAI-compatible route falls back to whatever
// plain-chat default that ROUTE happens to apply (commonly a small value
// like 4096), not the model's declared max_output_tokens from mivia's own
// catalog. For an always-thinking model (e.g. z.ai's GLM-5.3 family, which
// documents thinking as permanently on) that small default is consumed
// entirely by mandatory reasoning tokens before any answer text is
// produced, so the turn resolves to StopEmptyResponse no matter how many
// times retryOnEmptyResponse re-issues it. This heuristic is unverified
// against live traffic for every provider and should be tuned as real
// numbers come in.
func reasoningMaxTokensFloor(level reasoning.Level) int {
	switch level {
	case reasoning.XHigh, reasoning.Max:
		return 65536
	case reasoning.High:
		return 32768
	case reasoning.Medium:
		return 16384
	case reasoning.Low, reasoning.Minimal:
		return 8192
	default:
		// Off, or no reasoning level configured: a plain non-thinking turn
		// needs headroom for the response only.
		return 4096
	}
}

// reasoningFields resolves the dialect for one request and returns the fields
// to merge. A request-scoped dialect wins over the client default, so a model
// entry can name a wire shape its provider does not default to; the fall to the
// provider's vetted table is reasoning.Resolve's, so a client constructed
// without a default still encodes what config validated.
//
// The request is taken by pointer so the encoder can populate the SDK-shaped
// SDKReasoningEffort as a side effect; a value parameter would lose that
// side effect to the caller's copy, and the SDK adapter that consumes the
// request would never see the bridge's level->effort projection.
func (c *OpenAICompat) reasoningFields(req *Request) map[string]any {
	resolved := c.resolveReasoning(*req)
	req.SDKReasoningEffort = levelToSDKReasoningEffort(resolved.Level)
	return reasoningBodyFields(resolved.Dialect, resolved.Level)
}

// resolveReasoning resolves the wire dialect and level for one request,
// falling to the client's own default dialect when the request names none.
// Factored out of reasoningFields so marshalBody can resolve the same level
// to pick a max_tokens floor without duplicating the dialect fallback.
func (c *OpenAICompat) resolveReasoning(req Request) reasoning.Setting {
	dialect := req.ReasoningDialect
	if dialect == "" {
		dialect = c.reasoning
	}
	return reasoning.Resolve(c.name, reasoning.Setting{Level: req.ReasoningLevel, Dialect: dialect})
}

// ReasoningFields is the exported wrapper around reasoningFields. It
// runs the same encoding path marshalBody uses, and is exposed for
// tests and callers that need the resolved wire shape without
// performing an HTTP round-trip. The side effect of populating
// req.SDKReasoningEffort is identical: a call here is one more
// place the SDK-shaped effort is set on the request.
func (c *OpenAICompat) ReasoningFields(req *Request) map[string]any {
	return c.reasoningFields(req)
}

// levelToSDKReasoningEffort maps one resolved Level onto the SDK's
// ReasoningEffort vocabulary. The mapping mirrors
// internal/sdkadapter.LevelToReasoningEffort so the SDK-shaped request
// field carries the same wire value the bridge would return; the inline
// duplication avoids a new internal/provider -> internal/sdkadapter
// in-prefix edge (the bridge is the only package that may import both
// shapes, and provider is locked to in-prefix peers only by policy).
//
// An empty or unknown Level maps to the empty SDK effort, which the SDK
// reads as "send no reasoning field". Levels the SDK has no constant
// for (minimal, xhigh, max, auto) also map to empty: the user picked a level
// the SDK cannot carry, and the wire should not invent a value.
func levelToSDKReasoningEffort(l reasoning.Level) sdkshape.ReasoningEffort {
	switch l {
	case reasoning.Off:
		return sdkshape.ReasoningEffortNone
	case reasoning.Low:
		return sdkshape.ReasoningEffortLow
	case reasoning.Medium:
		return sdkshape.ReasoningEffortMedium
	case reasoning.High:
		return sdkshape.ReasoningEffortHigh
	default:
		return ""
	}
}
