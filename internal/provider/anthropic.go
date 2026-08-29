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
// anthropicMaxTokensFloor encodes a residual open question this
// implementation defends against rather than resolves (no live API access in
// this environment to resolve it empirically): the right max_tokens floor
// for adaptive thinking at each effort level. This is a conservative,
// tunable heuristic, not a documented Anthropic invariant.
//
// Thinking blocks are NOT replayed byte-for-byte across turns - only their
// plain display text survives into Response.ReasoningContent, since the
// rest of the codebase (the reasoning panel, session persistence) treats
// that field as plain text to render, not a structured payload. See
// anthropicThinkingDisplayText and anthropicSystemAndMessages' RoleAssistant
// case for the full rationale and a 2026-08-29 live test result on the
// tool-call-continuation shape this used to leave open.
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
	// cacheMarkers enables explicit cache_control breakpoints on the stable
	// request prefix. Anthropic caches ONLY on request, never implicitly, so
	// with this off the client re-bills the entire history on every step of
	// every tool loop. Off leaves the request body byte-identical to the
	// pre-marker layout, which is what [provider] prompt_cache = "off" buys.
	cacheMarkers bool
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
	return newAnthropicCompleter("anthropic", base, opts.APIKey, opts.DialContext, opts.CacheMarkersEnabled), nil
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
func newAnthropicCompleter(name, baseURL, apiKey string, dialContext func(ctx context.Context, network, addr string) (net.Conn, error), cacheMarkers bool) *AnthropicCompleter {
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
		reasoning:    reasoning.DialectAnthropicAdaptive,
		cacheMarkers: cacheMarkers,
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
	// The wire-stream transport: stream on the wire, non-stream contract on
	// the return path. chatTurnStream assembles the same *Response the plain
	// endpoint would return, and a nil StreamWriter means nothing is written
	// out as it arrives, so the caller cannot tell which wire shape served it.
	//
	// This is what keeps a long generation alive. A non-stream completion
	// sends no byte until it is finished, so its wait for response headers is
	// the model's whole thinking time - and the transport's header bound
	// (DefaultResponseHeaderTimeout) then caps every generation at that bound,
	// whatever request budget the operator configured. Streaming returns
	// headers at once and moves the work into the body phase, where the
	// watchdogs judge progress rather than total duration.
	if req.StreamTransport && req.StreamWriter == nil {
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
		return nil, asTransient(fmt.Errorf("%s: %w", c.name, markTransientReadDeadline(ctx, req.Timeout, err)))
	}
	defer func() { _ = resp.Body.Close() }()
	// Bounded like every other provider body read in this package: a peer that
	// accepted the request and then went silent must fail on the idle bound,
	// not hold the call to the transport's absolute wall. This is the
	// operationally common read - nested and subagent turns never stream.
	raw, err := io.ReadAll(io.LimitReader(wrapBodyWithIdleWatchdog(resp.Body, c.name), maxJSONResponseBytes))
	if err != nil {
		return nil, asTransient(fmt.Errorf("%s: read response: %w", c.name, markTransientReadDeadline(ctx, req.Timeout, err)))
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
	system, messages, anchors := anthropicSystemAndMessages(msgs)
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
	// Marking runs last so it sees the final system/messages values. When the
	// option is off nothing is touched and the body stays byte-identical to
	// the pre-marker layout.
	if c.cacheMarkers {
		markAnthropicCachePrefix(body, messages, anchors)
	}
	return body, nil
}

// markAnthropicCachePrefix places explicit cache_control breakpoints on the
// stable request prefix.
//
// Anthropic never caches implicitly: a request with no cache_control block is
// billed in full every time. In a multi-step tool loop that means re-paying
// for the entire transcript on every step, which is both the dominant cost of
// a long turn and the reason the per-step usage line reports no cache at all.
//
// Three of Anthropic's four permitted breakpoints are used, matching the
// policy the OpenAI-compatible client already applies through
// markStablePrefixCacheControl - one policy, two wire shapes, deliberately
// kept in step:
//
//   - the system prompt, which never changes within a session;
//   - the first user message, pinning the conversation's fixed head;
//   - a ROLLING breakpoint on the newest stable user turn, so the
//     append-only transcript behind it is cached instead of re-billed each
//     step. Moving the marker forward between steps is safe - cache_control
//     placement is excluded from prefix matching upstream.
//
// Assistant turns are never anchored: reasoning replay rewrites them, so they
// are not stable. Neither are host-injected ephemeral trailers, which is what
// the anchors slice tracks - see anthropicPendingTurn.stable.
func markAnthropicCachePrefix(body map[string]any, messages []map[string]any, anchors []int) {
	if system, ok := body["system"].(string); ok && system != "" {
		body["system"] = []any{map[string]any{
			"type":          "text",
			"text":          system,
			"cache_control": anthropicEphemeralCacheControl(),
		}}
	}
	firstUser := -1
	for i, msg := range messages {
		if msg["role"] == "user" {
			firstUser = i
			break
		}
	}
	if firstUser >= 0 {
		markAnthropicBlock(messages, anchors, firstUser)
	}
	for i := len(messages) - 1; i > firstUser; i-- {
		if messages[i]["role"] != "user" {
			continue
		}
		markAnthropicBlock(messages, anchors, i)
		return
	}
}

// markAnthropicBlock stamps the cache marker on message index's anchor block -
// the last block that will still be there, unchanged, next step. An index with
// no stable block (anchor < 0, a turn made only of ephemeral host text) is
// skipped rather than anchored on content that cannot cache.
func markAnthropicBlock(messages []map[string]any, anchors []int, index int) {
	if index < 0 || index >= len(anchors) {
		return
	}
	anchor := anchors[index]
	if anchor < 0 {
		return
	}
	blocks, ok := messages[index]["content"].([]map[string]any)
	if !ok || anchor >= len(blocks) {
		return
	}
	blocks[anchor]["cache_control"] = anthropicEphemeralCacheControl()
}

func anthropicEphemeralCacheControl() map[string]any {
	return map[string]any{"type": "ephemeral"}
}

// anthropicMaxTokens picks the wire max_tokens: the caller's explicit value
// when set, otherwise a conservative per-effort-level floor. See the
// AnthropicCompleter doc comment - this heuristic is unverified against a
// live API and should be tuned once real traffic data exists.
func anthropicMaxTokens(req Request, level reasoning.Level) int {
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		return *req.MaxTokens
	}
	return reasoning.OutputReserveFloor(level)
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
	// stable is the number of leading content blocks that are safe to anchor
	// a cache breakpoint on: every block contributed by a source message that
	// will still be present, unchanged, in the next request. Host-injected
	// ephemeral trailers (a named user message - the context summary or a
	// conclude nudge) do not recur, so a breakpoint placed on one is a
	// guaranteed miss on the following step. See anthropicCacheAnchor.
	stable int
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
func anthropicSystemAndMessages(msgs []Message) (system string, out []map[string]any, anchors []int) {
	var systemParts []string
	var cur *anthropicPendingTurn
	flush := func() {
		if cur != nil && len(cur.content) > 0 {
			out = append(out, map[string]any{"role": cur.role, "content": cur.content})
			anchors = append(anchors, cur.stable-1)
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
			cur.stable = len(cur.content)
		case RoleUser:
			openTurn("user")
			if strings.TrimSpace(m.Content) != "" {
				cur.content = append(cur.content, map[string]any{"type": "text", "text": m.Content})
				if m.Name == "" {
					cur.stable = len(cur.content)
				}
			}
		case RoleAssistant:
			openTurn("assistant")
			// No thinking block is replayed here - see
			// anthropicThinkingDisplayText's doc comment for why history
			// only carries display text, not the signed block Anthropic
			// would need to replay it unmodified. Whether omitting the
			// thinking block is actually safe on a turn that also carries
			// ToolCalls, immediately followed by a RoleTool message (the
			// common agentic shape - reasoning + tool call in one turn,
			// continued by its tool_result) was a long-standing open
			// question; a live manual test (claude-sonnet-5 via
			// llmproxycli's DialectAnthropicAdaptive, reasoning=high, two
			// sequential tool calls each continued by its tool_result,
			// 2026-08-29) completed with no error. The design doc's §3.5
			// example still shows a thinking block replayed alongside
			// tool_use as the normative shape; this code takes the opposite
			// path (omit rather than reconstruct-unsigned) because sending
			// a signature that doesn't match reconstructed content is the
			// failure mode Anthropic is documented to reject. The live test
			// is one manual session, not an automated regression harness -
			// see TestAnthropicReplayOfToolCallTurnWithReasoning, which
			// still pins this as current behavior rather than asserting it
			// is correct for every shape (long thinking traces, interleaved
			// thinking, and redacted_thinking remain unexercised).
			if strings.TrimSpace(m.Content) != "" {
				cur.content = append(cur.content, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				cur.content = append(cur.content, anthropicToolUseBlock(tc))
			}
		}
	}
	flush()
	return strings.Join(systemParts, "\n\n"), out, anchors
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
