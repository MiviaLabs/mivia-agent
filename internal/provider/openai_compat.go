package provider

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

const maxJSONResponseBytes = 8 << 20

// DefaultHTTPTimeout is the transport backstop for one provider request.
// The agent loop's request context remains the tighter per-call policy when
// one is supplied.
const DefaultHTTPTimeout = 15 * time.Minute

// DefaultResponseHeaderTimeout bounds only the accept-to-headers wait on a
// provider transport: the time from a sent request to the first response
// header bytes. The header wait stays fast even when generation is slow;
// body phases are covered by the stream watchdogs (idle_watchdog.go), and
// this bound sits under the 15-minute client wall above.
const DefaultResponseHeaderTimeout = 120 * time.Second

// OpenAICompat is a shared OpenAI-compatible chat client.
type OpenAICompat struct {
	name         string
	baseURL      string
	apiKey       string
	httpReferer  string
	xTitle       string
	extraHeaders map[string]string
	extraBody    map[string]any
	errorParser  func(statusCode int, body []byte) error
	client       *http.Client
	requestSeq   atomic.Uint64
	// cacheUsageEnabled gates parsing/reporting of provider usage fields into
	// Response.CacheUsage. Set once at construction from resolved config and
	// never mutated afterward - safe to read without synchronization. It
	// never changes what is sent on the wire: every provider this client
	// talks to caches automatically server-side with no request-side control.
	cacheUsageEnabled bool
	// cacheMarkersEnabled mirrors CompatOptions.CacheMarkersEnabled: when true
	// the client sends explicit cache_control markers (Anthropic-style) on the
	// stable prefix. Immutable mirror of a construction-time option, safe to
	// read without synchronization. The openrouter factory sets it whenever
	// [provider] prompt_cache is not "off"; implicit-cache providers
	// (deepseek, zai, ollama) never set it.
	cacheMarkersEnabled bool
	// reasoning is this provider's default wire dialect for reasoning control.
	// Set once at construction and never mutated. Empty means the provider has
	// no vetted default, so only a request naming its own dialect sends
	// anything.
	reasoning reasoning.Dialect
	// replayReasoning gates emission of assistant reasoning_content on the wire.
	// Default false: non-adopting providers never see the field, so request
	// bodies stay byte-identical. Set once at construction and never mutated.
	replayReasoning bool
	// replayReasoningField is the replay wire field; empty = "reasoning_content".
	// Set once at construction and never mutated.
	replayReasoningField string
	// rejectReasoningLessToolTurns drops assistant tool-call turns that lack
	// reasoning_content (D2 documented-400 gate). Independent of replay:
	// DeepSeek sets both; z.ai sets replay only so reasoning=off tool turns
	// still ship. Set once at construction and never mutated.
	rejectReasoningLessToolTurns bool
	// contextAccounting mirrors CompatOptions.ContextAccounting. Set once at
	// construction and never mutated; safe to read without synchronization.
	contextAccounting ContextAccountingProfile
	// sendSessionUserKey mirrors CompatOptions.SendSessionUserKey: when true
	// and a request carries Request.SessionID, the client emits a stable
	// hash of it as the OpenAI-compatible "user" field, so a provider that
	// keys routing stickiness on that field (OpenRouter) can route
	// follow-up requests in the same session to the same warm upstream.
	// Default false: non-adopting providers never see the field, so request
	// bodies stay byte-identical. Set once at construction and never mutated.
	sendSessionUserKey bool
	// streamHostile remembers a provider that cannot answer a stream request:
	// it either rejected the stream shape with a JSON rejection status, or
	// stalled a stream attempt without ever sending one data line. Once true,
	// the stream-transport path goes straight to the non-stream request for
	// the process lifetime of the client. atomic.Bool: turns run on many
	// goroutines sharing one client.
	streamHostile atomic.Bool
}

// CompatOptions configures an OpenAI-compatible client.
//
// ExtraHeaders and ExtraBody are copied by the options constructors. Reserved
// request fields are validated when a request is built.
type CompatOptions struct {
	Name         string
	BaseURL      string
	APIKey       string
	HTTPReferer  string
	XTitle       string
	ExtraHeaders map[string]string
	ExtraBody    map[string]any
	ErrorParser  func(statusCode int, body []byte) error
	// NonRetryable classifies an error response as permanent so the transport
	// stops retrying it. It is consulted only for statuses the shared policy
	// already considers retryable, and nil keeps that policy unchanged.
	NonRetryable func(statusCode int, body []byte) bool
	// CacheUsageEnabled gates capture of provider-reported prompt-cache usage
	// accounting into Response.CacheUsage. It never changes the outgoing
	// request.
	CacheUsageEnabled bool
	// CacheMarkersEnabled requests explicit cache_control markers on the
	// stable prefix. Default false. The openrouter factory enables it when
	// [provider] prompt_cache != "off"; implicit-cache providers leave it
	// off so their bodies stay byte-identical.
	CacheMarkersEnabled bool
	// Reasoning is this provider's default reasoning wire dialect, used when a
	// request carries a level but names no dialect of its own. Empty means the
	// provider has no vetted default and an unqualified level sends nothing.
	Reasoning reasoning.Dialect
	// RequiresReasoningReplay reports whether this provider's wire dialect
	// requires the assistant reasoning_content to be echoed back verbatim on
	// subsequent tool-call turns (DeepSeek thinking mode, z.ai preserved
	// thinking). Default false: the field is never emitted, so existing request
	// bodies are byte-identical.
	RequiresReasoningReplay bool
	// ReplayReasoningField is the replay wire field; empty = "reasoning_content".
	ReplayReasoningField string
	// RejectReasoningLessToolTurns is the documented-400 DROP gate (DeepSeek
	// ONLY). When true, assistant tool-call turns with empty ReasoningContent
	// are dropped with their tool results at emit. Independent of
	// RequiresReasoningReplay: z.ai sets replay without this bit so a
	// reasoning=off multi-step tool run still ships those turns.
	RejectReasoningLessToolTurns bool
	// ContextAccounting declares how this provider's server bills prompt
	// context (see ContextAccountingProfile). The zero value is the
	// conservative "bill everything" default, so a factory that leaves this
	// unset behaves exactly as before the field existed.
	ContextAccounting ContextAccountingProfile
	// SendSessionUserKey opts into emitting a hashed session-stickiness key
	// as the wire "user" field (see OpenAICompat.sendSessionUserKey). Only
	// the openrouter and llmgateway factories set this true.
	SendSessionUserKey bool
	// DialContext pins every dial for keyless loopback clients; nil keeps the
	// default dialer on a fresh clone of the base transport.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewOpenAICompatWithOptions constructs an OpenAI-compatible client from
// extensible options. Maps are copied before the client accepts requests.
func NewOpenAICompatWithOptions(opts CompatOptions) *OpenAICompat {
	retry := defaultRetryOptions()
	retry.NonRetryable = opts.NonRetryable
	c := &OpenAICompat{
		name:                         opts.Name,
		baseURL:                      strings.TrimRight(opts.BaseURL, "/"),
		apiKey:                       opts.APIKey,
		httpReferer:                  opts.HTTPReferer,
		xTitle:                       opts.XTitle,
		extraHeaders:                 cloneMap(opts.ExtraHeaders),
		extraBody:                    cloneBodyMap(opts.ExtraBody),
		errorParser:                  opts.ErrorParser,
		cacheUsageEnabled:            opts.CacheUsageEnabled,
		cacheMarkersEnabled:          opts.CacheMarkersEnabled,
		reasoning:                    opts.Reasoning,
		replayReasoning:              opts.RequiresReasoningReplay,
		replayReasoningField:         opts.ReplayReasoningField,
		rejectReasoningLessToolTurns: opts.RejectReasoningLessToolTurns,
		contextAccounting:            opts.ContextAccounting,
		sendSessionUserKey:           opts.SendSessionUserKey,
		client: &http.Client{
			Timeout:       DefaultHTTPTimeout,
			Transport:     newRetryRoundTripper(compatBaseRoundTripper(opts.DialContext), retry),
			CheckRedirect: checkNoReplayRedirect,
		},
	}
	// Every OpenAI-compatible provider gets a default error parser that never forwards the provider's own message text; providers override it via CompatOptions.ErrorParser.
	if c.errorParser == nil {
		c.errorParser = openaiErrorParser
	}
	return c
}

// NewOpenAICompat constructs a client with sensible retry defaults.
// Deprecated: use NewOpenAICompatWithOptions.
func NewOpenAICompat(name, baseURL, apiKey, httpReferer, xTitle string) *OpenAICompat {
	return NewOpenAICompatWithOptions(CompatOptions{Name: name, BaseURL: baseURL, APIKey: apiKey, HTTPReferer: httpReferer, XTitle: xTitle})
}

// NewOpenAICompatWithRetry constructs a client with custom retry options.
// Deprecated: use NewOpenAICompatWithOptionsAndRetry.
func NewOpenAICompatWithRetry(name, baseURL, apiKey, httpReferer, xTitle string, opts *retryOptions) *OpenAICompat {
	return NewOpenAICompatWithOptionsAndRetry(CompatOptions{Name: name, BaseURL: baseURL, APIKey: apiKey, HTTPReferer: httpReferer, XTitle: xTitle}, opts)
}

// NewOpenAICompatWithOptionsAndRetry constructs a client with custom retry options.
func NewOpenAICompatWithOptionsAndRetry(options CompatOptions, opts *retryOptions) *OpenAICompat {
	if opts == nil {
		opts = &retryOptions{}
	}
	baseOpts := defaultRetryOptions()
	if opts.MaxRetries > 0 {
		baseOpts.MaxRetries = opts.MaxRetries
	}
	if opts.BaseDelay > 0 {
		baseOpts.BaseDelay = opts.BaseDelay
	}
	if opts.MaxDelay > 0 {
		baseOpts.MaxDelay = opts.MaxDelay
	}
	baseOpts.NonRetryable = options.NonRetryable
	c := &OpenAICompat{
		name:                         options.Name,
		baseURL:                      strings.TrimRight(options.BaseURL, "/"),
		apiKey:                       options.APIKey,
		httpReferer:                  options.HTTPReferer,
		xTitle:                       options.XTitle,
		extraHeaders:                 cloneMap(options.ExtraHeaders),
		extraBody:                    cloneBodyMap(options.ExtraBody),
		errorParser:                  options.ErrorParser,
		cacheUsageEnabled:            options.CacheUsageEnabled,
		cacheMarkersEnabled:          options.CacheMarkersEnabled,
		reasoning:                    options.Reasoning,
		replayReasoning:              options.RequiresReasoningReplay,
		replayReasoningField:         options.ReplayReasoningField,
		rejectReasoningLessToolTurns: options.RejectReasoningLessToolTurns,
		contextAccounting:            options.ContextAccounting,
		sendSessionUserKey:           options.SendSessionUserKey,
		client: &http.Client{
			Timeout:       DefaultHTTPTimeout,
			Transport:     newRetryRoundTripper(compatBaseRoundTripper(options.DialContext), baseOpts),
			CheckRedirect: checkNoReplayRedirect,
		},
	}
	if c.errorParser == nil {
		c.errorParser = openaiErrorParser
	}
	return c
}

type chatRequestBody struct {
	Model       string       `json:"model"`
	Messages    []apiMessage `json:"messages"`
	Stream      bool         `json:"stream"`
	Temperature *float64     `json:"temperature,omitempty"`
	MaxTokens   *int         `json:"max_tokens,omitempty"`
	Tools       []ToolSpec   `json:"tools,omitempty"`
	ToolChoice  any          `json:"tool_choice,omitempty"`
}

// streamToolCallDelta is an OpenAI-compatible streaming tool_calls fragment.
type streamToolCallDelta struct {
	// Index is a pointer to detect absence: some upstreams omit it, and a
	// plain int would decode every such fragment to 0, collapsing distinct
	// calls onto one accumulator.
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// reasoningDetailWire is one reasoning_details entry; text or summary types.
type reasoningDetailWire struct {
	Type    string `json:"type,omitempty"`
	Text    string `json:"text,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type chatResponseBody struct {
	Choices []struct {
		Message struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			Reasoning        string                `json:"reasoning"`
			ReasoningDetails []reasoningDetailWire `json:"reasoning_details"`
			ToolCalls        []ToolCall            `json:"tool_calls"`
			WebSearch        []WebSearchResult     `json:"web_search"`
		} `json:"message"`
		Delta struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			Reasoning        string                `json:"reasoning"`
			ReasoningDetails []reasoningDetailWire `json:"reasoning_details"`
			ToolCalls        []streamToolCallDelta `json:"tool_calls"`
			WebSearch        []WebSearchResult     `json:"web_search"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	WebSearch []WebSearchResult `json:"web_search"`
	// Usage is decoded from both the full non-stream response body and every
	// individual SSE chunk during streaming - a provider that honors
	// stream_options.include_usage (or includes it unconditionally) may put
	// it on a chunk whose choices array is otherwise empty.
	Usage *usageWire `json:"usage,omitempty"`
}

// ReasoningPolicy reports c's construction-time reasoning-replay wire
// contract, implementing ReasoningPolicyAware.
func (c *OpenAICompat) ReasoningPolicy() ReasoningPolicy {
	return ReasoningPolicy{
		RequiresReplay:      c.replayReasoning,
		RejectReasoningLess: c.rejectReasoningLessToolTurns,
	}
}

// cacheUsage derives Response.CacheUsage from a decoded usage object,
// honoring cacheUsageEnabled. It is the single conversion point shared by
// the streaming and non-streaming response paths.
func (c *OpenAICompat) cacheUsage(usage *usageWire) CacheUsage {
	if !c.cacheUsageEnabled {
		return CacheUsage{}
	}
	style := CacheStyleImplicit
	if c.cacheMarkersEnabled {
		style = CacheStyleExplicit
	}
	return deriveCacheUsage(usage, style)
}

// Chat non-streaming text-only convenience.
func (c *OpenAICompat) Chat(ctx context.Context, req Request) (string, error) {
	req.Stream = false
	req.StreamWriter = nil
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// ChatTurn completion supporting tool_calls. When req.Stream is true, uses SSE
// and writes content deltas to req.StreamWriter (if set) as they arrive.
func (c *OpenAICompat) ChatTurn(ctx context.Context, req Request) (*Response, error) {
	if req.Stream {
		return c.chatTurnStream(ctx, req)
	}
	if req.StreamTransport && req.StreamWriter == nil {
		return c.chatTurnStreamTransport(ctx, req)
	}
	callCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()
	body, err := c.doJSON(callCtx, req)
	if err != nil {
		return nil, err
	}
	if len(body.Choices) == 0 {
		return nil, fmt.Errorf("%s: empty choices in response", c.name)
	}
	ch := body.Choices[0]
	// Normalize tool call types.
	for i := range ch.Message.ToolCalls {
		if ch.Message.ToolCalls[i].Type == "" {
			ch.Message.ToolCalls[i].Type = "function"
		}
	}
	webSearch := body.WebSearch
	if webSearch == nil {
		webSearch = ch.Message.WebSearch
	}
	return &Response{
		Content:          ch.Message.Content,
		ReasoningContent: resolveReasoningContent(ch.Message.ReasoningContent, ch.Message.Reasoning, ch.Message.ReasoningDetails),
		ToolCalls:        ch.Message.ToolCalls,
		FinishReason:     ch.FinishReason,
		WebSearch:        webSearch,
		CacheUsage:       c.cacheUsage(body.Usage),
		TokenUsage:       deriveTokenUsage(body.Usage),
	}, nil
}

// resolveReasoningContent surfaces the strongest reasoning signal a provider
// returned for one assistant message, in precedence order: the canonical
// resolveReasoningContent picks the response reasoning with precedence
// reasoning_content > reasoning > reasoning_details concatenation.
// resolveReasoningContent and retryWithoutStreaming live in
// openai_compat_retry.go.

// ChatStream streams SSE text deltas to w. With tools present, uses ChatTurn
// streaming so tool_calls still assemble correctly while content is live.
func (c *OpenAICompat) ChatStream(ctx context.Context, req Request, w io.Writer) (string, error) {
	callCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()
	if len(req.Tools) > 0 {
		req.Stream = true
		req.StreamWriter = w
		resp, err := c.ChatTurn(callCtx, req)
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}
	req.Stream = true
	resp, req, err := c.doChatRequest(callCtx, req)
	if err != nil {
		// asTransient mirrors the tools branch (ChatTurn -> chatTurnStream)
		// and the doChatRequest contract: each call site applies its own
		// transient decision. It is a no-op for permanent errors.
		return "", asTransient(err)
	}
	defer resp.Body.Close()

	return c.readStream(callCtx, req, c.wrapWithIdleWatchdog(resp.Body), w)
}

// retryWithoutStreaming re-asks for a turn the stream delivered nothing for.
//
// It rebuilds the request field by field rather than copying it, so every
// request-shaping field has to be repeated here. Omitting the reasoning pair
// would silently downgrade the model on exactly the turn that already produced
// nothing.
