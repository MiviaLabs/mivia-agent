package agent

import (
	"encoding/json"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// turnRequestDefaults is the per-turn request shape the completer
// wrapper injects because the SDK loop does not carry it.
type turnRequestDefaults struct {
	reasoning             reasoning.Setting
	maxTokens             *int
	temperature           *float64
	requestTimeout        time.Duration
	disableProviderReplay bool
	sessionID             string
	// streamTransport wires the provider's wire-stream transport (SSE on the
	// wire, non-stream contract on the return path) into every non-streaming
	// turn. mergeTurnDefaults applies it only when the request is still
	// non-streaming, so a live streaming turn always wins.
	streamTransport bool
}

// translateAgentLoopRequest projects an SDK Request onto a CLI
// Request. Model, Temperature, MaxTokens, Stream, Timeout,
// DisableProviderReplay, and SessionID pass through; Messages convert
// through sdkMessageToCLI; ReasoningEffort inverts to ReasoningLevel
// and ReasoningDialect maps by string. Tools stay nil: the SDK loop
// builds its own definitions from the registry, and re-translating
// the SDK's ToolDefinition list into the CLI's map-shaped ToolSpec is
// lossy and unneeded on this path. StreamWriter stays nil for the
// same reason - the wrapper's Chat path is non-streaming.
func translateAgentLoopRequest(req sdkshape.Request) provider.Request {
	return provider.Request{
		Model:                 req.Model,
		Messages:              sdkMessagesToCLI(req.Messages),
		Temperature:           req.Temperature,
		MaxTokens:             req.MaxTokens,
		Stream:                req.Stream,
		Timeout:               req.Timeout,
		DisableProviderReplay: req.DisableProviderReplay,
		ReasoningLevel:        sdkEffortToLevel(req.ReasoningEffort),
		ReasoningDialect:      reasoning.Dialect(req.ReasoningDialect),
		SessionID:             req.SessionID,
		Tools:                 sdkToolDefsToCLI(req.Tools),
	}
}

// sdkToolDefsToCLI converts the SDK loop's tool definitions onto the
// CLI's OpenAI-shaped ToolSpec map, the same shape
// tools.Registry.OpenAITools publishes: without this conversion the
// CLI completer's request carried no tool surface at all, so the
// provider never saw the tools the loop offered.
//
// The schema bytes round-trip as json.RawMessage: passing the bytes
// through verbatim (instead of unmarshalling into a map and
// remarshalling) preserves unknown shapes (null, primitives,
// non-object payloads) and avoids key reordering or float-precision
// drift. An empty schema falls back to an empty parameters object
// so the tool stays advertised under its name rather than vanishing.
func sdkToolDefsToCLI(defs []sdkshape.ToolDefinition) []provider.ToolSpec {
	if len(defs) == 0 {
		return nil
	}
	out := make([]provider.ToolSpec, 0, len(defs))
	for _, d := range defs {
		var params any = map[string]any{}
		if len(d.Schema) > 0 {
			params = json.RawMessage(d.Schema)
		}
		out = append(out, provider.ToolSpec{
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

// mergeTurnDefaults folds the wrapper's per-turn defaults into a
// converted request. The SDK loop never sets these request fields
// (runChat builds Model, Messages, Tools only), so the defaults win
// whenever the SDK left the field zero; an explicit SDK value - none
// today - would take precedence.
//
// Reasoning.Level and Reasoning.Dialect are checked independently:
// the AND-clause (both zero) used to miss the case where the SDK
// sent one of the two and left the other zero, losing the wrap-time
// default for the missing half. Independent checks honour each
// default independently.
func mergeTurnDefaults(req provider.Request, d turnRequestDefaults) provider.Request {
	if req.ReasoningLevel == "" {
		req.ReasoningLevel = d.reasoning.Level
	}
	if req.ReasoningDialect == "" {
		req.ReasoningDialect = d.reasoning.Dialect
	}
	if req.MaxTokens == nil {
		req.MaxTokens = d.maxTokens
	}
	if req.Temperature == nil {
		req.Temperature = d.temperature
	}
	if req.Timeout <= 0 {
		req.Timeout = d.requestTimeout
	}
	req.DisableProviderReplay = req.DisableProviderReplay || d.disableProviderReplay
	if req.SessionID == "" {
		req.SessionID = d.sessionID
	}
	// Only a still-non-streaming request takes the wire-stream transport.
	// applyStreaming runs after this merge on the TUI path and turns Stream
	// back on, and ChatTurn dispatches Stream before StreamTransport, so a
	// live streaming turn keeps its writer and its wire shape.
	if d.streamTransport && !req.Stream && req.StreamWriter == nil {
		req.StreamTransport = true
	}
	return req
}

// sdkEffortToLevel inverts the SDK's four-value ReasoningEffort onto
// the CLI's seven-value Level. An unknown effort maps to the empty
// Level (unset), which sends no reasoning field - the honest
// fail-open for a vocabulary the CLI does not carry, matching the
// encoder's treatment of an unset dial.
func sdkEffortToLevel(e sdkshape.ReasoningEffort) reasoning.Level {
	switch e {
	case sdkshape.ReasoningEffortNone:
		return reasoning.Off
	case sdkshape.ReasoningEffortLow:
		return reasoning.Low
	case sdkshape.ReasoningEffortMedium:
		return reasoning.Medium
	case sdkshape.ReasoningEffortHigh:
		return reasoning.High
	default:
		return ""
	}
}

// sdkMessagesToCLI converts a slice of SDK messages to CLI messages.
// A nil slice converts to nil so an empty request stays empty.
func sdkMessagesToCLI(msgs []sdkshape.Message) []provider.Message {
	if msgs == nil {
		return nil
	}
	out := make([]provider.Message, len(msgs))
	for i, m := range msgs {
		out[i] = sdkMessageToCLI(m)
	}
	return out
}

// sdkMessageToCLI converts one SDK message to a CLI message. Role,
// Content, Name, ToolCallID, and ReasoningContent pass through;
// ToolCalls convert through sdkToolCallToCLI.
func sdkMessageToCLI(m sdkshape.Message) provider.Message {
	calls := make([]provider.ToolCall, len(m.ToolCalls))
	for i, tc := range m.ToolCalls {
		calls[i] = sdkToolCallToCLI(tc)
	}
	return provider.Message{
		Role:             string(m.Role),
		Content:          m.Content,
		ToolCalls:        calls,
		ToolCallID:       m.ToolCallID,
		Name:             m.Name,
		ReasoningContent: m.ReasoningContent,
	}
}

// sdkToolCallToCLI converts one SDK tool call to the CLI's
// OpenAI-compatible shape: Type is "function" and the SDK's byte
// Arguments become the CLI's string Arguments.
func sdkToolCallToCLI(tc sdkshape.ToolCall) provider.ToolCall {
	var call provider.ToolCall
	call.ID = tc.ID
	call.Type = "function"
	call.Function.Name = tc.Name
	call.Function.Arguments = string(tc.Arguments)
	return call
}

// cliMessagesToSDK converts a slice of CLI messages to SDK messages.
// A nil slice converts to nil so an empty history stays empty.
func cliMessagesToSDK(msgs []provider.Message) []sdkshape.Message {
	if msgs == nil {
		return nil
	}
	out := make([]sdkshape.Message, len(msgs))
	for i, m := range msgs {
		out[i] = cliMessageToSDK(m)
	}
	return out
}

// cliMessageToSDK converts one CLI message to an SDK message. It is
// the inverse of sdkMessageToCLI; the CLI's string Arguments become
// the SDK's byte Arguments.
//
// Empty Arguments round-trip as nil (not []byte("")) so the SDK's
// "no payload yet" reading matches convertToSDKResponse's own
// handling at line 410 below. A literal "" survives the round-trip as
// "no JSON" and breaks the SDK's compiled-schema validator with
// ErrArgumentValidation, replacing the legacy "error: ..." envelope
// with the SDK's "[tool-error]" notice - a silent shape mismatch.
func cliMessageToSDK(m provider.Message) sdkshape.Message {
	calls := make([]sdkshape.ToolCall, len(m.ToolCalls))
	for i, tc := range m.ToolCalls {
		var args []byte
		if tc.Function.Arguments != "" {
			args = []byte(tc.Function.Arguments)
		}
		calls[i] = sdkshape.ToolCall{
			Index:     i,
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		}
	}
	return sdkshape.Message{
		Role:             sdkshape.Role(m.Role),
		Content:          m.Content,
		ToolCalls:        calls,
		ToolCallID:       m.ToolCallID,
		Name:             m.Name,
		ReasoningContent: m.ReasoningContent,
	}
}

// convertToSDKResponse projects a CLI Response onto an SDK
// Response. The helper is file-local (lowercase) because no other
// wrapper in this package needs it. Tool calls are copied by ID
// and Name; the CLI's Arguments string is wrapped in []byte
// because the SDK's ToolCall carries a byte payload. An empty
// CLI Arguments becomes a nil slice so the SDK's Validate
// treats it as "no payload yet" rather than "zero-length payload".
func convertToSDKResponse(r provider.Response) sdkshape.Response {
	identified := identifiedToolCalls(r.ToolCalls)
	toolCalls := make([]sdkshape.ToolCall, len(identified))
	for i, tc := range identified {
		toolCalls[i] = sdkshape.ToolCall{
			Index: i,
			ID:    tc.ID,
			Name:  tc.Function.Name,
		}
		if tc.Function.Arguments != "" {
			toolCalls[i].Arguments = []byte(tc.Function.Arguments)
		}
	}
	return sdkshape.Response{
		Message: sdkshape.Message{
			Role:             sdkshape.RoleAssistant,
			Content:          r.Content,
			ReasoningContent: r.ReasoningContent,
			ToolCalls:        toolCalls,
		},
		ToolCalls:    toolCalls,
		FinishReason: r.FinishReason,
		Usage:        cliTokenUsageToSDK(r.TokenUsage),
	}
}

// cliTokenUsageToSDK projects the CLI's TokenUsage onto the SDK's
// Usage. The CLI gates on Reported (a zero Reported means the
// provider said nothing); the SDK has no such flag, so an unreported
// CLI usage converts to the zero SDK Usage - the same "no observation"
// value the SDK treats as absent.
func cliTokenUsageToSDK(t provider.TokenUsage) sdkshape.Usage {
	if !t.Reported {
		return sdkshape.Usage{}
	}
	return sdkshape.Usage{
		PromptTokens:     t.InputTokens,
		CompletionTokens: t.OutputTokens,
		TotalTokens:      t.InputTokens + t.OutputTokens,
	}
}
