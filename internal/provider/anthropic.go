package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// anthropicAPIVersion is the fixed anthropic-version header value. Anthropic
// requires it on every request; it names a wire-format contract version, not
// a model version, and does not change when a new model ships.
const anthropicAPIVersion = "2023-06-01"

// AnthropicCompleter speaks Anthropic's native Messages API
// (POST /v1/messages) directly, translating this package's OpenAI-shaped
// Request/Message/ToolCall/Response types to and from Anthropic's own wire
// format. Unlike every other builtin provider it does not wrap OpenAICompat:
// Anthropic's request/response shape (system/messages/content-blocks,
// thinking blocks, output_config.effort) is structurally different from the
// OpenAI-compatible chat/completions shape the other clients share, so it has
// nothing to inherit from that type.
//
// See docs/plans/anthropic-provider-and-reasoning-plan.md for the researched
// wire format, the required headers, and two residual open questions this
// implementation defends against rather than resolves (no live API access in
// this environment to resolve them empirically):
//   - whether an adaptive-thinking block carries a "signature" field the way
//     pre-4.6 manual-thinking blocks did. Mitigation: thinking blocks are
//     round-tripped as raw JSON (see anthropicThinkingBlocksToReasoningContent
//     / anthropicThinkingBlocksFromReasoningContent) rather than parsed field
//     by field, so whatever Anthropic actually sends survives replay
//     unmodified regardless of its exact schema.
//   - the right max_tokens floor for adaptive thinking at each effort level
//     (anthropicMaxTokensFloor). This is a conservative, tunable heuristic,
//     not a documented Anthropic invariant.
type AnthropicCompleter struct {
	name       string
	baseURL    string
	apiKey     string
	httpClient *http.Client
	// reasoning is this client's resolved default wire dialect (normally
	// reasoning.DialectAnthropicAdaptive, read from the vetted table in
	// internal/reasoning so config validation and this client agree on the
	// same value). A request naming its own ReasoningDialect overrides it.
	reasoning reasoning.Dialect
}

// NewAnthropic returns a native Anthropic Messages API completer.
func NewAnthropic(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("anthropic")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "anthropic")
		}
		base = descriptor.DefaultURL
	}
	return newAnthropicCompleter("anthropic", base, opts.APIKey, opts.DialContext), nil
}

// newAnthropicCompleter builds an AnthropicCompleter against an arbitrary
// base URL and API key - no providerregistry lookup, no fallback. This is
// the constructor NewAnthropic itself uses, and the one
// internal/provider/llmproxycli.go uses to speak native Anthropic wire
// format through a LOCAL PROXY's own base_url/api_key_env (llmproxycli
// dispatches per-model to a completer built this way for any model whose
// config entry sets reasoning_dialect = "anthropic_adaptive" - see
// llmproxycli.go's llmProxyDispatchCompleter).
//
// reasoning is unconditionally DialectAnthropicAdaptive, never derived from
// name via defaultReasoningDialect: this type speaks exactly one wire shape
// regardless of which provider name constructed it. A caller building one
// under a different provider name (llmproxycli) must not silently get
// llmproxycli's own default dialect (DialectOpenAI) instead - that would
// send OpenAI-shaped reasoning fields into a client that marshals an
// Anthropic-shaped request body, which is not a dialect mismatch a wire
// encoder can recover from.
func newAnthropicCompleter(name, baseURL, apiKey string, dialContext func(ctx context.Context, network, addr string) (net.Conn, error)) *AnthropicCompleter {
	retry := defaultRetryOptions()
	return &AnthropicCompleter{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout:       DefaultHTTPTimeout,
			Transport:     newRetryRoundTripper(compatBaseRoundTripper(dialContext), retry),
			CheckRedirect: checkNoReplayRedirect,
		},
		reasoning: reasoning.DialectAnthropicAdaptive,
	}
}

// Name implements Completer.
func (c *AnthropicCompleter) Name() string { return c.name }

// Chat implements Completer: a plain-text turn discarding tool calls.
func (c *AnthropicCompleter) Chat(ctx context.Context, req Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// ChatTurn implements Completer: a non-stream turn that may return tool
// calls, and surfaces a safety-classifier refusal as FinishReasonRefusal
// rather than as an error (Anthropic returns HTTP 200 with an empty or
// partial content array on a refusal, not a failure status). When
// req.Stream is set with a non-nil req.StreamWriter, the turn streams -
// this is how the SDK-backed agent loop gets both live text output and a
// full tool-call-carrying Response from one call (see
// internal/agent/agentloop_completer.go's applyStreaming).
func (c *AnthropicCompleter) ChatTurn(ctx context.Context, req Request) (*Response, error) {
	body, err := c.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	if req.Stream && req.StreamWriter != nil {
		return c.chatTurnStream(ctx, req, body)
	}
	raw, err := c.do(ctx, req, body)
	if err != nil {
		return nil, err
	}
	var wire anthropicResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", c.name, err)
	}
	return anthropicResponseToProvider(wire), nil
}

// newHTTPRequest builds one POST /v1/messages request with the required
// headers and, when req.Timeout is set, a context bound to it. Shared by the
// non-stream (do) and streaming (chatTurnStream) send paths so both build the
// exact same request shape.
func (c *AnthropicCompleter) newHTTPRequest(ctx context.Context, req Request, body map[string]any) (*http.Request, context.CancelFunc, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: encode request: %w", c.name, err)
	}
	cancel := func() {}
	if req.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(encoded))
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("%s: build request: %w", c.name, err)
	}
	c.setHeaders(httpReq)
	return httpReq, cancel, nil
}

// do sends one non-stream request and returns the raw response body, or a
// wrapped error for a non-2xx status (status and error type only - never the
// provider's own message text, matching the privacy convention every other
// provider's error parser in this package follows).
func (c *AnthropicCompleter) do(ctx context.Context, req Request, body map[string]any) ([]byte, error) {
	httpReq, cancel, err := c.newHTTPRequest(ctx, req, body)
	if err != nil {
		return nil, err
	}
	defer cancel()
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", c.name, err)
	}
	if err := anthropicErrorFromBody(c.name, resp.StatusCode, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// setHeaders sets the two headers every Anthropic request requires
// (anthropic-version, x-api-key) plus content-type. No anthropic-beta header
// is set: adaptive thinking and output_config.effort are both GA on
// claude-sonnet-5 and need none.
func (c *AnthropicCompleter) setHeaders(httpReq *http.Request) {
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
	httpReq.Header.Set("x-api-key", c.apiKey)
}

// buildRequestBody translates req into Anthropic's wire shape: system pulled
// to the top level, messages translated and role-coalesced (see
// anthropicSystemAndMessages), tools and tool_choice translated from the
// OpenAI shape, and the resolved reasoning dialect's fields merged in via the
// same reasoningBodyFields every other dialect uses (Anthropic's shape - a
// top-level thinking object plus a nested output_config.effort - is just
// another case in that switch, not a separate encoder).
func (c *AnthropicCompleter) buildRequestBody(req Request) (map[string]any, error) {
	// Every OpenAI-compatible provider gets these two repairs for free via
	// toAPIMessages (api_message.go), which this native client does not call
	// (it builds Anthropic's own wire shape directly, not an OpenAI-style
	// body). Applying them here closes the same gap: an empty assistant turn
	// (no content, no tool calls - the shape a genuinely empty provider
	// response leaves behind) would otherwise still open an "assistant"
	// pending turn in anthropicSystemAndMessages and then contribute zero
	// content blocks, silently causing the NEXT user/tool message to start a
	// fresh "user" turn instead of extending the one before the empty
	// assistant message - two adjacent Anthropic "user" messages, which 400s
	// on role alternation the same way the uncoalesced-tool-results bug did.
	// An orphaned tool_use/tool_result pair (a torn session write) is the
	// same class of poisoned-history shape and gets the same treatment.
	msgs := RepairToolPairing(DropEmptyAssistantTurns(req.Messages))
	system, messages := anthropicSystemAndMessages(msgs)
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages to send")
	}
	dialect := req.ReasoningDialect
	if dialect == "" {
		dialect = c.reasoning
	}
	resolved := reasoning.Resolve(c.name, reasoning.Setting{Level: req.ReasoningLevel, Dialect: dialect})

	body := map[string]any{
		"model":      req.Model,
		"max_tokens": anthropicMaxTokens(req, resolved.Level),
		"messages":   messages,
	}
	if system != "" {
		body["system"] = system
	}
	if tools := anthropicTools(req.Tools); len(tools) > 0 {
		body["tools"] = tools
	}
	if choice := anthropicToolChoice(req.ToolChoice); choice != nil {
		body["tool_choice"] = choice
	}
	// req.Temperature is deliberately NEVER forwarded, unlike every other
	// dialect's request builder. Anthropic's claude-sonnet-5 (and the rest
	// of the current model generation) rejects a non-default temperature
	// outright - HTTP 400 - and this code has no way to tell "the caller's
	// value happens to equal Anthropic's default" from "the caller wants a
	// different value": Request.Temperature is a bare *float64 carrying
	// whatever a session/model-wide [chat] setting resolved to, not a
	// signal of intent specific to this provider. Omitting the field is
	// always safe (Anthropic runs at its own default) and is Anthropic's
	// own documented recommendation for steering behavior on models where
	// sampling parameters are removed - use effort/prompting instead. Step-5
	// bug audit caught an earlier version of this that forwarded the value
	// verbatim: with a config carrying a non-default temperature (e.g. the
	// bug report this feature exists to fix, [chat] temperature = 0.0),
	// every request 400s exactly as before, silently defeating the whole
	// native-Anthropic-routing fix.
	for k, v := range reasoningBodyFields(resolved.Dialect, resolved.Level) {
		body[k] = v
	}
	return body, nil
}

// anthropicMaxTokens picks the wire max_tokens: the caller's explicit value
// when set, otherwise a conservative per-effort-level floor. See the
// AnthropicCompleter doc comment - this heuristic is unverified against a
// live API and should be tuned once real traffic data exists.
func anthropicMaxTokens(req Request, level reasoning.Level) int {
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		return *req.MaxTokens
	}
	return anthropicMaxTokensFloor(level)
}

func anthropicMaxTokensFloor(level reasoning.Level) int {
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

// anthropicTools translates the OpenAI-shaped tools[] entries
// ({"type":"function","function":{"name","description","parameters"}}, see
// internal/tools.Registry.OpenAITools) into Anthropic's flatter
// {"name","description","input_schema"} shape. An entry missing the
// "function" wrapper or a name is skipped rather than sent malformed -
// defensive only; every caller in this tree builds tools through
// Registry.OpenAITools, which always includes both.
func anthropicTools(specs []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		fn, ok := spec["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		tool := map[string]any{"name": name}
		if desc, ok := fn["description"].(string); ok {
			tool["description"] = desc
		}
		if params, ok := fn["parameters"].(map[string]any); ok {
			tool["input_schema"] = params
		} else {
			tool["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, tool)
	}
	return out
}

// anthropicToolChoice translates Request.ToolChoice ("auto", "none", or
// empty) into Anthropic's {"type": ...} shape. Empty returns nil (omit the
// field entirely, Anthropic's own default is "auto"). Any other value is
// passed through as {"type": value} rather than silently dropped, so a
// caller-set "any" or "tool" (values this codebase's Request.ToolChoice
// comment documents as OpenAI-only today) at least reaches the wire visibly
// instead of vanishing.
func anthropicToolChoice(choice string) map[string]any {
	switch choice {
	case "":
		return nil
	case "none":
		return map[string]any{"type": "none"}
	default:
		return map[string]any{"type": choice}
	}
}

// anthropicPendingTurn accumulates one Anthropic wire message's content
// blocks while anthropicSystemAndMessages walks the OpenAI-shaped history.
type anthropicPendingTurn struct {
	role    string
	content []map[string]any
}

// anthropicSystemAndMessages translates an OpenAI-shaped message history into
// Anthropic's system string plus messages array.
//
// Anthropic requires the top-level messages array to strictly alternate
// role "user" / role "assistant", with every tool_result for one assistant
// turn living inside a single "user" message's content array. This
// codebase's history is one Message per wire turn (see
// mivia-ai-sdk/agentloop/toolcall.go's runToolCalls, which appends one
// RoleTool Message per parallel tool call), so a naive 1:1 translation would
// emit N consecutive Anthropic "user" messages for an N-call parallel tool
// turn - a guaranteed 400 ("messages: roles must alternate") on the very
// first multi-tool-call turn. The fix generalizes past just RoleTool: RoleTool
// and RoleUser both map to Anthropic role "user" (a tool result and a
// following user-authored or injected notice message, e.g. the
// prompt-too-long compaction notice in internal/agent/agentloop_run.go,
// which is deliberately RoleUser), so this function coalesces every run of
// consecutive Messages that map to the same Anthropic role into one wire
// message with multiple content blocks, in original order - not just
// consecutive RoleTool runs.
func anthropicSystemAndMessages(msgs []Message) (system string, out []map[string]any) {
	var systemParts []string
	var cur *anthropicPendingTurn
	flush := func() {
		if cur != nil && len(cur.content) > 0 {
			out = append(out, map[string]any{"role": cur.role, "content": cur.content})
		}
		cur = nil
	}
	openTurn := func(role string) {
		if cur == nil || cur.role != role {
			flush()
			cur = &anthropicPendingTurn{role: role}
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			if strings.TrimSpace(m.Content) != "" {
				systemParts = append(systemParts, m.Content)
			}
		case RoleTool:
			openTurn("user")
			cur.content = append(cur.content, map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			})
		case RoleUser:
			openTurn("user")
			if strings.TrimSpace(m.Content) != "" {
				cur.content = append(cur.content, map[string]any{"type": "text", "text": m.Content})
			}
		case RoleAssistant:
			openTurn("assistant")
			cur.content = append(cur.content, anthropicThinkingBlocksFromReasoningContent(m.ReasoningContent)...)
			if strings.TrimSpace(m.Content) != "" {
				cur.content = append(cur.content, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				cur.content = append(cur.content, anthropicToolUseBlock(tc))
			}
		}
	}
	flush()
	return strings.Join(systemParts, "\n\n"), out
}

// anthropicToolUseBlock builds one tool_use content block from an
// OpenAI-shaped ToolCall, preserving its ID verbatim (Anthropic requires a
// later tool_result.tool_use_id to exactly match). Arguments is a JSON-encoded
// string on the OpenAI shape; Anthropic's "input" is the decoded object
// itself, not a string.
func anthropicToolUseBlock(tc ToolCall) map[string]any {
	var input any
	if strings.TrimSpace(tc.Function.Arguments) != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
			input = nil
		}
	}
	if input == nil {
		input = map[string]any{}
	}
	return map[string]any{
		"type":  "tool_use",
		"id":    tc.ID,
		"name":  tc.Function.Name,
		"input": input,
	}
}

// anthropicThinkingBlocksFromReasoningContent reconstructs the raw thinking
// content block(s) previously stored verbatim in Message.ReasoningContent
// (see anthropicThinkingBlocksToReasoningContent) so they replay to Anthropic
// byte-identical to how they were received - including any field (e.g. a
// signature) this codebase does not itself interpret. Empty or
// unparseable input returns nil: a Message from history predating this
// provider, or a plain non-thinking turn, carries no thinking block, and a
// corrupted value degrades to "send no thinking block" rather than failing
// the whole turn.
func anthropicThinkingBlocksFromReasoningContent(reasoningContent string) []map[string]any {
	if strings.TrimSpace(reasoningContent) == "" {
		return nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(reasoningContent), &blocks); err != nil {
		return nil
	}
	return blocks
}

// anthropicThinkingBlocksToReasoningContent is the inverse: it JSON-encodes
// the raw thinking blocks from a response verbatim for storage in
// Response.ReasoningContent / the next turn's Message.ReasoningContent. Empty
// input returns "" (no thinking happened, or display was omitted with an
// empty thinking block - either way, nothing to replay).
func anthropicThinkingBlocksToReasoningContent(blocks []json.RawMessage) string {
	if len(blocks) == 0 {
		return ""
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return ""
	}
	return string(encoded)
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
		ReasoningContent: anthropicThinkingBlocksToReasoningContent(thinkingBlocks),
		ToolCalls:        toolCalls,
		FinishReason:     anthropicFinishReason(wire.StopReason),
		TokenUsage: TokenUsage{
			Reported:     true,
			InputTokens:  wire.Usage.InputTokens,
			OutputTokens: wire.Usage.OutputTokens,
		},
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
