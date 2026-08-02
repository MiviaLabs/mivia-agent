package provider

import "github.com/MiviaLabs/mivia-agent/internal/reasoning"

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
	case reasoning.DialectThinking:
		return map[string]any{"thinking": thinkingObject(level)}
	case reasoning.DialectThinkingEffort:
		if level == reasoning.Off {
			// The thinking object alone disables. Pairing it with an effort
			// value would put two contradictory instructions in one body.
			return map[string]any{"thinking": thinkingObject(level)}
		}
		return map[string]any{
			"thinking":         thinkingObject(level),
			"reasoning_effort": string(level),
		}
	default:
		// reasoning.DialectNone, and any dialect this client does not know:
		// fail closed rather than guess a wire shape.
		return nil
	}
}

func thinkingObject(level reasoning.Level) map[string]any {
	if level == reasoning.Off {
		return map[string]any{"type": "disabled"}
	}
	return map[string]any{"type": "enabled"}
}

// reasoningFields resolves the dialect for one request and returns the fields
// to merge. A request-scoped dialect wins over the client default, so a model
// entry can name a wire shape its provider does not default to.
func (c *OpenAICompat) reasoningFields(req Request) map[string]any {
	dialect := req.ReasoningDialect
	if dialect == "" {
		dialect = c.reasoning
	}
	return reasoningBodyFields(dialect, req.ReasoningLevel)
}
