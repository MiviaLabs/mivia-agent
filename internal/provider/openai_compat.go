package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// read without synchronization. Default false, and no current provider
	// factory sets it - poolside/laguna does NOT support caching on
	// OpenRouter - so this is infrastructure for future cache-capable models.
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
	// stable prefix. Default false; no factory sets it (laguna has no
	// OpenRouter caching) - infrastructure for future cache-capable models.
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
		client: &http.Client{
			Timeout:       DefaultHTTPTimeout,
			Transport:     newRetryRoundTripper(http.DefaultTransport, retry),
			CheckRedirect: checkNoReplayRedirect,
		},
	}
	// Every OpenAI-compatible provider gets a default error parser that
	// never forwards the provider's own message text. Providers that need
	// richer diagnostics (e.g. z.ai with its numeric body codes) override
	// this by setting ErrorParser in their CompatOptions.
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
		client: &http.Client{
			Timeout:       DefaultHTTPTimeout,
			Transport:     newRetryRoundTripper(http.DefaultTransport, baseOpts),
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
func resolveReasoningContent(reasoningContent, reasoning string, details []reasoningDetailWire) string {
	if reasoningContent != "" {
		return reasoningContent
	}
	if reasoning != "" {
		return reasoning
	}
	var b strings.Builder
	for _, d := range details {
		if d.Text != "" {
			b.WriteString(d.Text)
		} else if d.Summary != "" {
			b.WriteString(d.Summary)
		}
	}
	return b.String()
}

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
	httpReq, err := c.newRequest(callCtx, req)
	if err != nil {
		return "", err
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		err = markTransientReadDeadline(callCtx, req.Timeout, err)
		return "", fmt.Errorf("%s: request failed: %w", c.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", c.httpError(resp)
	}

	return c.readStream(callCtx, req, resp.Body, w)
}

func (c *OpenAICompat) readStream(ctx context.Context, req Request, body io.Reader, w io.Writer) (string, error) {
	var full strings.Builder
	received := false
	sc := bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	// The cancel check runs before the scan so a line the scanner accepted
	// still reaches the writer if the context fires while it is processed.
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return full.String(), fmt.Errorf("%s: stream read: %w (request deadline %s)", c.name, markTransientReadDeadline(ctx, req.Timeout, ctx.Err()), deadlineLabel(req.Timeout))
			}
			return full.String(), ctx.Err()
		default:
		}
		if !sc.Scan() {
			break
		}
		line := sc.Text()
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if c.errorParser != nil {
			parserBody := []byte(data)
			if len(parserBody) > 4096 {
				parserBody = parserBody[:4096]
			}
			if err := c.errorParser(http.StatusOK, parserBody); err != nil {
				return full.String(), err
			}
		}
		var chunk chatResponseBody
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			// A usage-only chunk is a completion signal. Do not replay the turn.
			if chunk.Usage != nil {
				received = true
			}
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		streamDelta := chunk.Choices[0].Delta
		// Any delivered wire shape (content/reasoning/finish/usage) must not re-bill.
		if deltaCountsAsReceived(delta, streamDelta.ReasoningContent, streamDelta.Reasoning, chunk.Choices[0].FinishReason, streamDelta.ReasoningDetails, chunk.Usage) {
			received = true
		}
		if delta == "" {
			continue
		}
		full.WriteString(delta)
		if w != nil {
			if _, err := io.WriteString(w, delta); err != nil {
				return full.String(), err
			}
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return full.String(), fmt.Errorf("%s: stream read: %w (request deadline %s)", c.name, markTransientReadDeadline(ctx, req.Timeout, err), deadlineLabel(req.Timeout))
		}
		return full.String(), fmt.Errorf("%s: stream read: %w", c.name, err)
	}
	if full.Len() == 0 && !received {
		if req.DisableProviderReplay {
			return "", fmt.Errorf("%s: stream delivered no response", c.name)
		}
		return c.retryWithoutStreaming(ctx, req, w)
	}
	return full.String(), nil
}

// retryWithoutStreaming re-asks for a turn the stream delivered nothing for.
//
// It rebuilds the request field by field rather than copying it, so every
// request-shaping field has to be repeated here. Omitting the reasoning pair
// would silently downgrade the model on exactly the turn that already produced
// nothing.
func (c *OpenAICompat) retryWithoutStreaming(ctx context.Context, req Request, w io.Writer) (string, error) {
	content, err := c.Chat(ctx, Request{
		Model:            req.Model,
		Messages:         req.Messages,
		Temperature:      req.Temperature,
		MaxTokens:        req.MaxTokens,
		ToolChoice:       req.ToolChoice,
		Timeout:          req.Timeout,
		Stream:           false,
		ReasoningLevel:   req.ReasoningLevel,
		ReasoningDialect: req.ReasoningDialect,
	})
	if err != nil {
		return content, err
	}
	// The fallback fires only when nothing was live-written, so writing the
	// answer here delivers it exactly once. Mirrors the tool-capable path in
	// chatTurnStream. Nil-safe: ChatStream allows a nil writer.
	if w != nil {
		if _, werr := io.WriteString(w, content); werr != nil {
			return content, werr
		}
	}
	return content, nil
}
