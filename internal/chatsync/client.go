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
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var (
	ErrConflict     = errors.New("chatsync: session conflict (409)")
	ErrUnauthorized = errors.New("chatsync: unauthorized (401)")
	ErrNotFound     = errors.New("chatsync: session not found (404)")

	// ErrBadRequest reports a 400. It is a distinct sentinel because the
	// settled policy splits on it: a sequence-gap 400 rebases and continues,
	// while every other 400 is poison a retry cannot fix.
	ErrBadRequest = errors.New("chatsync: bad request (400)")

	// ErrTranscriptConflict reports that the server already holds events at
	// the sequences this client just sent, with bodies that are not the ones
	// it sent. Terminal for sync.
	ErrTranscriptConflict = errors.New("chatsync: the server holds a different transcript at these sequences")

	// ErrNoTokenProvider reports a construction attempt with no way to
	// authenticate.
	ErrNoTokenProvider = errors.New("chatsync: a token provider is required")

	// ErrEmptyToken reports a token provider that returned no error and no
	// token. Sending the request anyway would omit the Authorization header,
	// so this fails closed instead.
	ErrEmptyToken = errors.New("chatsync: the token provider returned an empty token")

	// ErrInvalidPathID reports an id this client refused to interpolate into
	// a request path. See validatePathID.
	ErrInvalidPathID = errors.New("chatsync: invalid id for request path")
)

// pathIDPattern is the conservative allowlist every id this client places
// into a URL path must match: ASCII letters, digits, underscore, dot,
// hyphen. No '/', no '?', no '#', no whitespace, no control characters.
//
// sessionID and inputID both cross the wire before they reach here -
// sessionID from a server CreateSession/attach response or the persisted
// identity file, inputID from the server's own NextInput response, which
// ConsumeInput then re-embeds into a SECOND request's path
// (internal/chatsync/poller.go's pollOnce). A hostile or compromised server
// could return an id containing "../", a query string, or CRLF, and every
// %s that goes straight into fmt.Sprintf("/v1/.../%s/...", id) would carry
// it into the request line unescaped. Rejecting outright (not
// url.PathEscape-ing) is deliberate: an escaped path segment still reaches
// the server as a literal weird-looking id, but rejecting here means a
// malformed id never leaves this process as a request at all.
var pathIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,256}$`)

// validatePathID rejects an id unsafe to interpolate into a request path.
func validatePathID(id string) error {
	if !pathIDPattern.MatchString(id) {
		return fmt.Errorf("%w: %q", ErrInvalidPathID, id)
	}
	return nil
}

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

// BadRequestError conveys a 400 from the API.
//
// The distinction it carries is load-bearing. Retrying an identical body
// against a 400 resubmits a request the server has already judged malformed,
// for as long as the process lives; treating a sequence gap as fatal
// "guarantees the failure it is trying to avoid" (chat-sync-cli-slice.md:163).
// A caller therefore needs to tell the two apart, which a bare error string
// cannot express.
type BadRequestError struct {
	StatusCode int
	Message    string
	Code       string
}

func (e *BadRequestError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("chatsync bad request (%d): %s (code: %s)", e.StatusCode, e.Message, e.Code)
	}
	return fmt.Sprintf("chatsync bad request (%d): %s", e.StatusCode, e.Message)
}

func (e *BadRequestError) Is(target error) bool {
	return target == ErrBadRequest
}

// IsSequenceComplaint reports whether the server named a sequence problem.
//
// The check is on the message because the API returns no machine-readable code
// for it - the live guard probe pins only that the message names the sequence
// (internal/chatsync/live_guards_test.go, "a forward sequence gap is
// rejected"). It is deliberately a NECESSARY, not a sufficient, condition: the
// caller must still re-read the session and confirm the server's mark actually
// moves the batch, because a message match alone would rebase on a client bug.
func (e *BadRequestError) IsSequenceComplaint() bool {
	return strings.Contains(strings.ToLower(e.Message), "sequence")
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

	// authLost latches once the token provider reports a failure no retry
	// can fix. Every later request then fails fast instead of re-entering
	// the refresh path, which is what "stop sync" means at this layer: the
	// session cannot prompt mid-chat, so the only safe answer is to stop
	// talking to the API rather than to keep trying with a dead session.
	authLost atomic.Bool
}

// ClientOptions configures Client. It deliberately carries no token
// provider: the provider is a positional argument to NewClient so that a
// missing one is a compile error, not a zero value that silently sends
// conversation content with no Authorization header.
type ClientOptions struct {
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// NewClient constructs a Client. tokens is required. A nil provider is
// refused rather than tolerated: this client uploads conversation content,
// and an unauthenticated upload is the fail-open case this argument exists
// to make impossible.
func NewClient(tokens TokenProvider, opts ClientOptions) (*Client, error) {
	if tokens == nil {
		return nil, ErrNoTokenProvider
	}
	baseURL := strings.TrimRight(opts.BaseURL, "/")
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: opts.Timeout}
	}
	return &Client{
		baseURL:       baseURL,
		httpClient:    httpClient,
		tokenProvider: tokens,
	}, nil
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
	if err := validatePathID(sessionID); err != nil {
		return nil, err
	}
	var out Session
	path := fmt.Sprintf("/v1/chat-sessions/%s", sessionID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppendEvents appends a batch of events to a session stream.
func (c *Client) AppendEvents(ctx context.Context, sessionID string, events []EventItem) (*AppendResult, error) {
	if err := validatePathID(sessionID); err != nil {
		return nil, err
	}
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
	if err := validatePathID(sessionID); err != nil {
		return nil, err
	}
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
	if err := validatePathID(sessionID); err != nil {
		return nil, err
	}
	var out NextInput
	path := fmt.Sprintf("/v1/chat-sessions/%s/inputs/next?waitSeconds=%d", sessionID, waitSeconds)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ConsumeInput acknowledges receipt of a remote input via POST /v1/chat-sessions/{id}/inputs/{inputID}/consume.
//
// inputID is server-supplied (the id NextInput's response just handed back),
// so it is validated exactly like sessionID before it is re-embedded into
// this second request's path - see validatePathID's doc comment.
func (c *Client) ConsumeInput(ctx context.Context, sessionID, inputID string) (*SessionInput, error) {
	if err := validatePathID(sessionID); err != nil {
		return nil, err
	}
	if err := validatePathID(inputID); err != nil {
		return nil, err
	}
	var out SessionInput
	path := fmt.Sprintf("/v1/chat-sessions/%s/inputs/%s/consume", sessionID, inputID)
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Heartbeat sends a status heartbeat via POST /v1/chat-sessions/{id}/heartbeat.
func (c *Client) Heartbeat(ctx context.Context, sessionID, status string) (*Session, error) {
	if err := validatePathID(sessionID); err != nil {
		return nil, err
	}
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
	if err := validatePathID(sessionID); err != nil {
		return nil, err
	}
	var out Session
	path := fmt.Sprintf("/v1/chat-sessions/%s/end", sessionID)
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// doJSON runs one request and implements the settled 401 policy: refresh once
// and retry, exactly once.
//
// The retry is not repeated. A second 401 after a fresh token is not an
// expired token, it is a rejected session or a captive portal answering 401
// for everything, and retrying either of those in a loop is how a live
// session gets destroyed.
func (c *Client) doJSON(ctx context.Context, method, path string, reqBody any, respBody any) error {
	if c.authLost.Load() {
		return ErrAuthStop
	}
	err := c.execRequest(ctx, method, path, reqBody, respBody, false)
	if err == nil || !errors.Is(err, ErrUnauthorized) {
		return err
	}
	return c.execRequest(ctx, method, path, reqBody, respBody, true)
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

	tok, err := c.tokenProvider(ctx, forceRefresh)
	if err != nil {
		if errors.Is(err, ErrAuthStop) {
			c.authLost.Store(true)
		}
		return nil, fmt.Errorf("get auth token: %w", err)
	}
	if tok == "" {
		return nil, ErrEmptyToken
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return req, nil
}

// AuthLost reports whether this client has latched a fatal authentication
// failure. The session loop reads it to stop the pusher, poller and
// heartbeat instead of retrying a session that cannot come back.
func (c *Client) AuthLost() bool { return c.authLost.Load() }

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
	// 413 and 422 join 400 as poison. The deployed API answers 400 for an
	// oversized payload (pinned by TestLiveChatSessionPayloadBoundIsAClientError),
	// so this is not today's behaviour - it is the fail-safe for the day it
	// changes. Without it those statuses fall to the default branch, which is
	// not ErrBadRequest, so the flush retries a body the server can never
	// accept, for the life of the process.
	//
	// Deliberately NOT every 4xx: 408 and 429 are the server asking us to try
	// again later, and poisoning them would turn a transient slowdown into a
	// permanent stop.
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return &BadRequestError{
			StatusCode: resp.StatusCode,
			Message:    msg,
			Code:       errEnv.Error,
		}
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
