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
			Timeout:       httpClientTimeout(),
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
	// DisableProviderReplay is carried on the context because that is where
	// the two components that must honor it read it: the retry round tripper
	// (which would otherwise replay this exact POST up to five times, each a
	// separate billable generation) and the redirect guard. Without the stamp
	// the flag reached the wire as a suggestion. Both send paths - do and
	// chatTurnStream - build their request here, so one stamp covers both.
	if req.DisableProviderReplay {
		ctx = context.WithValue(ctx, disableProviderReplayContextKey{}, true)
	}
	// The body is the authority on which wire shape this request takes: both
	// send paths build it here, and only chatTurnStream sets stream. A
	// non-stream Messages request answers all at once, so its header wait is
	// the generation and carries no header bound (header_bound.go).
	if streamed, _ := body["stream"].(bool); !streamed {
		ctx = withGenerationHeaderPhase(ctx)
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
		// markTransientReadDeadline needs the ARMED request context, not the caller's.
		// newHTTPRequest builds the req.Timeout child internally, and httpReq.Context()
		// is that child: passing the parent instead inverts the test, marking a spent
		// request budget transient and a tighter parent bound permanent.
		return nil, asTransient(fmt.Errorf("%s: %w", c.name, markTransientReadDeadline(httpReq.Context(), req.Timeout, err)))
	}
	defer func() { _ = resp.Body.Close() }()
	// Bounded like every other provider body read in this package: a peer that
	// accepted the request and then went silent must fail on the idle bound,
	// not hold the call to the transport's absolute wall. This is the
	// operationally common read - nested and subagent turns never stream.
	raw, err := io.ReadAll(io.LimitReader(wrapBodyWithIdleWatchdog(resp.Body, c.name), maxJSONResponseBytes))
	if err != nil {
		return nil, asTransient(fmt.Errorf("%s: read response: %w", c.name, markTransientReadDeadline(httpReq.Context(), req.Timeout, err)))
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
