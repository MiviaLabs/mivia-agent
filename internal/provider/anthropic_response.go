package provider

// Anthropic response decoding: the Messages API reply, its usage accounting,
// and its error envelope. Split out of anthropic.go alongside
// anthropic_request.go; the streaming decoder is anthropic_stream.go.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// anthropicThinkingDisplayText extracts and concatenates the plain
// human-readable "thinking" field from each raw thinking content block, for
// storage in Response.ReasoningContent / Message.ReasoningContent. Empty
// input, or every block's thinking field being empty (Anthropic's default
// "omitted" display setting - see reasoningBodyFields' DialectAnthropicAdaptive
// case, which requests "summarized" instead precisely so this is non-empty),
// returns "".
//
// This field is NOT provider-specific in the rest of the codebase:
// internal/agent's emitReasoning (loop_step.go) and every downstream UI
// consumer (internal/uiadapter, internal/ui/component/transcript) treat
// ReasoningContent as plain display text to render verbatim, with zero
// parsing - the same assumption DeepSeek/z.ai's reasoning_content already
// relies on. An earlier version of this code instead stored the raw
// JSON-encoded thinking block (including Anthropic's opaque signature field)
// here for replay purposes; that broke the reasoning panel, which rendered
// the JSON envelope as if it were prose. Fixed by extracting display text
// here and NOT attempting byte-perfect thinking-block replay at all (see
// anthropicSystemAndMessages' RoleAssistant case) - Anthropic tolerates a
// continued conversation with no thinking block on the replayed turn; it
// does not tolerate one whose signature no longer matches its content, which
// is the only alternative once the signature itself isn't stored anywhere
// display-safe.
func anthropicThinkingDisplayText(blocks []json.RawMessage) string {
	if len(blocks) == 0 {
		return ""
	}
	var parts []string
	for _, raw := range blocks {
		var block struct {
			Thinking string `json:"thinking"`
		}
		if json.Unmarshal(raw, &block) == nil && block.Thinking != "" {
			parts = append(parts, block.Thinking)
		}
	}
	return strings.Join(parts, "\n\n")
}

// anthropicResponse is the subset of Anthropic's non-stream Messages API
// response this client reads. Content is decoded as raw blocks first (see
// anthropicResponseToProvider) because block shape is polymorphic
// (text/thinking/tool_use) and thinking blocks must round-trip byte-identical
// rather than being re-derived from typed fields.
type anthropicResponse struct {
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      anthropicUsage    `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// promptTokens is the TRUE size of the prompt Anthropic priced, which is what
// token accounting and the context gauge mean by "input tokens".
//
// Anthropic's input_tokens counts only the UNCACHED remainder: tokens served
// from the cache are reported separately in cache_read_input_tokens, and
// tokens written to it in cache_creation_input_tokens. Reading input_tokens
// alone was correct only while this client sent no cache_control markers. The
// moment caching is on, a well-cached step reports a few percent of its real
// prompt, and internal/agent's Loop.Calibration - which divides that reported
// count by the host's own estimate - collapses toward its 0.2 floor and scales
// every future compaction estimate down with it, silently disabling the
// auto-compaction trigger. Summing the three fields keeps the count equal to
// the prompt actually sent, cached or not.
func (u anthropicUsage) promptTokens() int {
	return nonNegative(u.InputTokens) + nonNegative(u.CacheReadInputTokens) + nonNegative(u.CacheCreationInputTokens)
}

// cacheUsage reports this turn's prompt-cache accounting. Anthropic always
// speaks the explicit (marker) style, and a response is treated as reporting
// cache usage only when it actually carried one of the cache fields, so a
// non-cached deployment keeps the zero value and EmitCacheUsage stays silent
// rather than announcing "0% cached" on every turn.
func (u anthropicUsage) cacheUsage() CacheUsage {
	if u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
		return CacheUsage{}
	}
	return CacheUsage{
		Reported:          true,
		Style:             CacheStyleExplicit,
		InputTokens:       u.promptTokens(),
		CachedInputTokens: nonNegative(u.CacheReadInputTokens),
		CacheWriteTokens:  nonNegative(u.CacheCreationInputTokens),
	}
}

// anthropicBlockType peeks a content block's discriminator field without
// committing to its full shape - every Anthropic content block type carries
// "type", nothing else is guaranteed present.
type anthropicBlockType struct {
	Type string `json:"type"`
}

type anthropicTextBlock struct {
	Text string `json:"text"`
}

type anthropicToolUseBlockWire struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// anthropicResponseToProvider translates a decoded Anthropic response into
// this package's Response shape: text blocks concatenate into Content,
// tool_use blocks become ToolCalls (Arguments re-encoded as the JSON string
// the OpenAI shape expects), thinking blocks round-trip raw into
// ReasoningContent, and stop_reason maps onto FinishReason (see
// anthropicFinishReason - refusal becomes FinishReasonRefusal, not an error,
// per Anthropic returning HTTP 200 on a policy decline).
func anthropicResponseToProvider(wire anthropicResponse) *Response {
	var textParts []string
	var toolCalls []ToolCall
	var thinkingBlocks []json.RawMessage
	for _, raw := range wire.Content {
		var head anthropicBlockType
		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}
		switch head.Type {
		case "text":
			var block anthropicTextBlock
			if json.Unmarshal(raw, &block) == nil && block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			var block anthropicToolUseBlockWire
			if json.Unmarshal(raw, &block) != nil {
				continue
			}
			args := "{}"
			if len(block.Input) > 0 {
				args = string(block.Input)
			}
			tc := ToolCall{ID: block.ID, Type: "function"}
			tc.Function.Name = block.Name
			tc.Function.Arguments = args
			toolCalls = append(toolCalls, tc)
		case "thinking":
			thinkingBlocks = append(thinkingBlocks, raw)
		}
	}
	return &Response{
		Content:          strings.Join(textParts, ""),
		ReasoningContent: anthropicThinkingDisplayText(thinkingBlocks),
		ToolCalls:        toolCalls,
		FinishReason:     anthropicFinishReason(wire.StopReason),
		TokenUsage: TokenUsage{
			Reported:     true,
			InputTokens:  wire.Usage.promptTokens(),
			OutputTokens: nonNegative(wire.Usage.OutputTokens),
		},
		CacheUsage: wire.Usage.cacheUsage(),
	}
}

// anthropicFinishReason maps Anthropic's stop_reason vocabulary onto the
// FinishReason strings this codebase's other providers already produce
// (openai_compat.go's finish_reason passthrough): end_turn/stop_sequence to
// "stop" (internal/agent's schema-repair path and others treat "stop" as
// ordinary completion), max_tokens to "length" (the one value
// internal/subagents/multi_step_schema.go specifically switches on), tool_use
// to "tool_calls", and refusal to FinishReasonRefusal. Anything else -
// including pause_turn, which only occurs when a request declares
// server-side tools, which this client's callers do not - passes through
// unmodified rather than being mapped to a misleading "stop", so an
// unexpected value stays visible instead of silently looking like a normal
// completion.
func anthropicFinishReason(stopReason string) string {
	switch stopReason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case FinishReasonRefusal:
		return FinishReasonRefusal
	default:
		return stopReason
	}
}

// anthropicErrorEnvelope is Anthropic's standard error shape:
// {"type":"error","error":{"type":"...","message":"..."}}.
type anthropicErrorEnvelope struct {
	Error *struct {
		Type string `json:"type"`
	} `json:"error"`
}

// anthropicErrorFromBody classifies a non-2xx response into a wrapped error,
// naming only the HTTP status and, when available, Anthropic's own error
// type classification - never the message text, which may echo request
// content (the same privacy convention openaiErrorParser documents for the
// OpenAI-compatible providers). A 2xx status always returns nil: a refusal is
// HTTP 200 (see ChatTurn) and is not an error at this layer.
func anthropicErrorFromBody(name string, statusCode int, body []byte) error {
	if statusCode >= 200 && statusCode < 300 {
		return nil
	}
	if statusCode == http.StatusUnauthorized {
		return fmt.Errorf("%s: auth failed (HTTP %d) - check API key", name, statusCode)
	}
	var envelope anthropicErrorEnvelope
	if json.Unmarshal(body, &envelope) == nil && envelope.Error != nil && envelope.Error.Type != "" {
		return fmt.Errorf("%s: provider error (HTTP %d, type %s)", name, statusCode, envelope.Error.Type)
	}
	return fmt.Errorf("%s: provider error (HTTP %d)", name, statusCode)
}
