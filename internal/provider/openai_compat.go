package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const maxJSONResponseBytes = 8 << 20

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
}

// NewOpenAICompatWithOptions constructs an OpenAI-compatible client from
// extensible options. Maps are copied before the client accepts requests.
func NewOpenAICompatWithOptions(opts CompatOptions) *OpenAICompat {
	return &OpenAICompat{
		name:         opts.Name,
		baseURL:      strings.TrimRight(opts.BaseURL, "/"),
		apiKey:       opts.APIKey,
		httpReferer:  opts.HTTPReferer,
		xTitle:       opts.XTitle,
		extraHeaders: cloneMap(opts.ExtraHeaders),
		extraBody:    cloneBodyMap(opts.ExtraBody),
		errorParser:  opts.ErrorParser,
		client: &http.Client{
			Timeout:   180 * time.Second,
			Transport: newRetryRoundTripper(http.DefaultTransport, defaultRetryOptions()),
		},
	}
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
	return &OpenAICompat{
		name:         options.Name,
		baseURL:      strings.TrimRight(options.BaseURL, "/"),
		apiKey:       options.APIKey,
		httpReferer:  options.HTTPReferer,
		xTitle:       options.XTitle,
		extraHeaders: cloneMap(options.ExtraHeaders),
		extraBody:    cloneBodyMap(options.ExtraBody),
		errorParser:  options.ErrorParser,
		client: &http.Client{
			Timeout:   180 * time.Second,
			Transport: newRetryRoundTripper(http.DefaultTransport, baseOpts),
		},
	}
}

func (c *OpenAICompat) Name() string { return c.name }

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

type chatResponseBody struct {
	Choices []struct {
		Message struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			ToolCalls        []ToolCall        `json:"tool_calls"`
			WebSearch        []WebSearchResult `json:"web_search"`
		} `json:"message"`
		Delta struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []streamToolCallDelta `json:"tool_calls"`
			WebSearch        []WebSearchResult     `json:"web_search"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	WebSearch []WebSearchResult `json:"web_search"`
	Error     *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
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
		ReasoningContent: ch.Message.ReasoningContent,
		ToolCalls:        ch.Message.ToolCalls,
		FinishReason:     ch.FinishReason,
		WebSearch:        webSearch,
	}, nil
}

// ChatStream streams SSE text deltas to w. With tools present, uses ChatTurn
// streaming so tool_calls still assemble correctly while content is live.
func (c *OpenAICompat) ChatStream(ctx context.Context, req Request, w io.Writer) (string, error) {
	if len(req.Tools) > 0 {
		req.Stream = true
		req.StreamWriter = w
		resp, err := c.ChatTurn(ctx, req)
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}
	req.Stream = true
	httpReq, err := c.newRequest(ctx, req)
	if err != nil {
		return "", err
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("%s: request failed: %w", c.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", c.httpError(resp)
	}

	return c.readStream(ctx, req, resp.Body, w)
}

func (c *OpenAICompat) readStream(ctx context.Context, req Request, body io.Reader, w io.Writer) (string, error) {
	var full strings.Builder
	received := false
	sc := bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	for sc.Scan() {
		select {
		case <-ctx.Done():
			return full.String(), ctx.Err()
		default:
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
		if chunk.Error != nil && chunk.Error.Message != "" {
			return full.String(), fmt.Errorf("%s: %s", c.name, sanitizeErr(chunk.Error.Message))
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		received = true
		delta := chunk.Choices[0].Delta.Content
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
		return full.String(), fmt.Errorf("%s: stream read: %w", c.name, err)
	}
	if full.Len() == 0 && !received {
		return c.Chat(ctx, Request{
			Model:       req.Model,
			Messages:    req.Messages,
			Temperature: req.Temperature,
			MaxTokens:   req.MaxTokens,
			Stream:      false,
		})
	}
	return full.String(), nil
}

func (c *OpenAICompat) doJSON(ctx context.Context, req Request) (*chatResponseBody, error) {
	httpReq, err := c.newRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request failed: %w", c.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.httpError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", c.name, err)
	}
	if len(raw) > maxJSONResponseBytes {
		return nil, fmt.Errorf("%s: response exceeds %d byte limit", c.name, maxJSONResponseBytes)
	}
	if c.errorParser != nil {
		parserBody := raw
		if len(parserBody) > 4096 {
			parserBody = parserBody[:4096]
		}
		if err := c.errorParser(resp.StatusCode, parserBody); err != nil {
			return nil, err
		}
	}
	var body chatResponseBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", c.name, err)
	}
	if body.Error != nil && body.Error.Message != "" {
		return nil, fmt.Errorf("%s: %s", c.name, sanitizeErr(body.Error.Message))
	}
	return &body, nil
}

func (c *OpenAICompat) newRequest(ctx context.Context, req Request) (*http.Request, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("%s: model is required", c.name)
	}
	for key := range c.extraHeaders {
		for _, reserved := range []string{"Authorization", "Content-Type", "Accept", "Idempotency-Key"} {
			if strings.EqualFold(key, reserved) {
				return nil, fmt.Errorf("%s: extra header %q is reserved", c.name, key)
			}
		}
	}
	for key := range c.extraBody {
		switch key {
		case "model", "messages", "stream":
			return nil, fmt.Errorf("%s: extra body field %q is reserved", c.name, key)
		}
	}
	apiMsgs := toAPIMessages(req.Messages)
	payload := chatRequestBody{
		Model:       req.Model,
		Messages:    apiMsgs,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Tools:       req.Tools,
	}
	if req.ToolChoice != "" {
		payload.ToolChoice = req.ToolChoice
	} else if len(req.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	body := map[string]any{}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	for key, value := range c.extraBody {
		body[key] = value
	}
	raw, err = json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := c.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "application/json")
	// Retries may occur after the provider accepted the request. A stable
	// request key lets providers that support idempotency suppress duplicates.
	key := sha256.Sum256(raw)
	httpReq.Header.Set("Idempotency-Key", fmt.Sprintf("mivia-%d-%x", c.requestSeq.Add(1), key[:]))
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if c.httpReferer != "" {
		httpReq.Header.Set("HTTP-Referer", c.httpReferer)
	}
	if c.xTitle != "" {
		httpReq.Header.Set("X-Title", c.xTitle)
	}
	for key, value := range c.extraHeaders {
		httpReq.Header.Set(key, value)
	}
	return httpReq, nil
}

func (c *OpenAICompat) httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if c.errorParser != nil {
		if err := c.errorParser(resp.StatusCode, body); err != nil {
			return err
		}
	}
	msg := strings.TrimSpace(string(body))
	msg = sanitizeErr(msg)
	// Drain remaining response body so the TCP connection can be reused
	// by the HTTP transport. The caller will close via defer after this
	// returns; without draining, Go's transport opens a new connection.
	_, _ = io.CopyN(io.Discard, resp.Body, 64*1024)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: auth failed (HTTP %d) — check API key", c.name, resp.StatusCode)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s: rate limited (HTTP 429)", c.name)
	default:
		if msg == "" {
			return fmt.Errorf("%s: HTTP %d", c.name, resp.StatusCode)
		}
		return fmt.Errorf("%s: HTTP %d: %s", c.name, resp.StatusCode, msg)
	}
}

func sanitizeErr(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
