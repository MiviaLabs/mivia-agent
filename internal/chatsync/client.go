package chatsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrConflict     = errors.New("chatsync: session conflict (409)")
	ErrUnauthorized = errors.New("chatsync: unauthorized (401)")
	ErrNotFound     = errors.New("chatsync: session not found (404)")
)

// ConflictError conveys detailed 409 conflict payload from server.
type ConflictError struct {
	StatusCode int
	Message    string
	Code       string
}

func (e *ConflictError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("chatsync conflict (%d): %s (code: %s)", e.StatusCode, e.Message, e.Code)
	}
	return fmt.Sprintf("chatsync conflict (%d): %s", e.StatusCode, e.Message)
}

func (e *ConflictError) Is(target error) bool {
	return target == ErrConflict
}

// CreateSessionParams defines the request payload for registering a new chat session.
type CreateSessionParams struct {
	Title     string `json:"title"`
	CwdLabel  string `json:"cwdLabel,omitempty"`
	HostLabel string `json:"hostLabel,omitempty"`
}

// TokenProvider retrieves a bearer token for authentication.
type TokenProvider func(ctx context.Context, forceRefresh bool) (string, error)

// Client executes authenticated requests against /v1/chat-sessions.
type Client struct {
	baseURL       string
	httpClient    *http.Client
	tokenProvider TokenProvider
}

// ClientOptions configures Client.
type ClientOptions struct {
	BaseURL       string
	HTTPClient    *http.Client
	TokenProvider TokenProvider
	Timeout       time.Duration
}

// NewClient constructs a Client with the specified options.
func NewClient(opts ClientOptions) *Client {
	baseURL := strings.TrimRight(opts.BaseURL, "/")
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: opts.Timeout}
	}
	return &Client{
		baseURL:       baseURL,
		httpClient:    httpClient,
		tokenProvider: opts.TokenProvider,
	}
}

// CreateSession registers a new session via POST /v1/chat-sessions.
func (c *Client) CreateSession(ctx context.Context, params CreateSessionParams) (*Session, error) {
	var out Session
	if err := c.doJSON(ctx, http.MethodPost, "/v1/chat-sessions", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSession fetches session metadata via GET /v1/chat-sessions/{id}.
func (c *Client) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	var out Session
	path := fmt.Sprintf("/v1/chat-sessions/%s", sessionID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppendEvents appends a batch of events to a session stream.
func (c *Client) AppendEvents(ctx context.Context, sessionID string, events []EventItem) (*AppendResult, error) {
	var out AppendResult
	path := fmt.Sprintf("/v1/chat-sessions/%s/events", sessionID)
	body := map[string]any{"events": events}
	if err := c.doJSON(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetEvents reads back events by cursor via GET /v1/chat-sessions/{id}/events.
func (c *Client) GetEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]StoredEvent, error) {
	var out []StoredEvent
	vals := url.Values{}
	vals.Set("afterSeq", strconv.FormatInt(afterSeq, 10))
	if limit > 0 {
		vals.Set("limit", strconv.Itoa(limit))
	}
	path := fmt.Sprintf("/v1/chat-sessions/%s/events?%s", sessionID, vals.Encode())
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// NextInput polls for remote input via GET /v1/chat-sessions/{id}/inputs/next.
func (c *Client) NextInput(ctx context.Context, sessionID string, waitSeconds int) (*NextInput, error) {
	var out NextInput
	path := fmt.Sprintf("/v1/chat-sessions/%s/inputs/next?waitSeconds=%d", sessionID, waitSeconds)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ConsumeInput acknowledges receipt of a remote input via POST /v1/chat-sessions/{id}/inputs/{inputID}/consume.
func (c *Client) ConsumeInput(ctx context.Context, sessionID, inputID string) (*SessionInput, error) {
	var out SessionInput
	path := fmt.Sprintf("/v1/chat-sessions/%s/inputs/%s/consume", sessionID, inputID)
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Heartbeat sends a status heartbeat via POST /v1/chat-sessions/{id}/heartbeat.
func (c *Client) Heartbeat(ctx context.Context, sessionID, status string) (*Session, error) {
	var out Session
	path := fmt.Sprintf("/v1/chat-sessions/%s/heartbeat", sessionID)
	body := map[string]string{"status": status}
	if err := c.doJSON(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EndSession ends a session via POST /v1/chat-sessions/{id}/end.
func (c *Client) EndSession(ctx context.Context, sessionID string) (*Session, error) {
	var out Session
	path := fmt.Sprintf("/v1/chat-sessions/%s/end", sessionID)
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, reqBody any, respBody any) error {
	err := c.execRequest(ctx, method, path, reqBody, respBody, false)
	if err != nil && errors.Is(err, ErrUnauthorized) && c.tokenProvider != nil {
		return c.execRequest(ctx, method, path, reqBody, respBody, true)
	}
	return err
}

func (c *Client) execRequest(ctx context.Context, method, path string, reqBody, respBody any, forceRefresh bool) error {
	req, err := c.buildRequest(ctx, method, path, reqBody, forceRefresh)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if respBody != nil {
			if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
		}
		return nil
	}

	return parseErrorResponse(resp)
}

func (c *Client) buildRequest(ctx context.Context, method, path string, reqBody any, forceRefresh bool) (*http.Request, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.tokenProvider != nil {
		tok, err := c.tokenProvider(ctx, forceRefresh)
		if err != nil {
			return nil, fmt.Errorf("get auth token: %w", err)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	return req, nil
}

func parseErrorResponse(resp *http.Response) error {
	respBytes, _ := io.ReadAll(resp.Body)
	var errEnv ErrorEnvelope
	_ = json.Unmarshal(respBytes, &errEnv)

	msg := parseErrorMessage(errEnv.Message)
	if msg == "" {
		msg = errEnv.Error
	}
	if msg == "" {
		msg = string(respBytes)
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusConflict:
		return &ConflictError{
			StatusCode: resp.StatusCode,
			Message:    msg,
			Code:       errEnv.Error,
		}
	default:
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, msg)
	}
}

func parseErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return strings.Join(arr, "; ")
	}
	return string(raw)
}
