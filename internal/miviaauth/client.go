package miviaauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// requestTimeout bounds a single login/refresh/revoke call. It is
// deliberately short: this is an interactive credential exchange, never a
// long-running stream.
const requestTimeout = 10 * time.Second

// StatusError reports a non-2xx HTTP response from go-mivia's auth
// endpoints. Callers use errors.As to classify failures (e.g. 401 vs
// 429/503) without parsing go-mivia's JSON error envelope.
type StatusError struct {
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("miviaauth: server responded with status %d", e.StatusCode)
}

// Client talks to the go-mivia bearer-token CLI auth endpoints.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient validates baseURL and returns a Client. baseURL must be an
// absolute https URL, with one exception: a loopback address (127.0.0.1,
// ::1, or the literal "localhost") over plain http is accepted for local
// dev. This exception does NOT consult MIVIA_ALLOW_INSECURE_HTTP — that
// env var is scoped to credential-free LLM provider traffic; this endpoint
// carries a password and gets its own, narrower exception with no env
// override.
func NewClient(baseURL string) (*Client, error) {
	if _, err := config.ValidateHTTPSURL(baseURL); err == nil {
		return newClient(baseURL), nil
	}
	if config.IsOllamaLoopback(baseURL) {
		return newClient(baseURL), nil
	}
	return nil, fmt.Errorf("miviaauth: base url must be https (loopback http is allowed for local dev)")
}

func newClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// Login exchanges an email and password for a Token. password is converted
// to a string only at the JSON marshal call site below; it is never stored
// as a Go string anywhere else in this file.
func (c *Client) Login(ctx context.Context, email string, password []byte) (Token, error) {
	body, err := json.Marshal(loginRequest{Email: email, Password: string(password)})
	if err != nil {
		return Token{}, fmt.Errorf("miviaauth: encode login request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/auth/login", bytes.NewReader(body))
	if err != nil {
		return Token{}, fmt.Errorf("miviaauth: build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mivia-Auth-Transport", "cli")
	return c.doSessionRequest(req)
}

// Refresh exchanges a still-valid bearer for a new Token.
func (c *Client) Refresh(ctx context.Context, bearer string) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/auth/refresh", nil)
	if err != nil {
		return Token{}, fmt.Errorf("miviaauth: build refresh request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	return c.doSessionRequest(req)
}

// Revoke invalidates bearer server-side. Success is HTTP 204.
func (c *Client) Revoke(ctx context.Context, bearer string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/auth/revoke", nil)
	if err != nil {
		return fmt.Errorf("miviaauth: build revoke request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("miviaauth: revoke request: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("miviaauth: revoke failed: %w", &StatusError{StatusCode: resp.StatusCode})
	}
	return nil
}

// doSessionRequest performs req and parses the shared login/refresh
// response shape into a Token. Non-2xx responses are reported as a
// *StatusError; network-level failures (including context cancellation)
// are returned as a plain wrapped error so callers can tell the two apart.
func (c *Client) doSessionRequest(req *http.Request) (Token, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("miviaauth: request failed: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("miviaauth: request failed: %w", &StatusError{StatusCode: resp.StatusCode})
	}

	var wire sessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return Token{}, fmt.Errorf("miviaauth: decode response: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, wire.Session.ExpiresAt)
	if err != nil {
		return Token{}, fmt.Errorf("miviaauth: parse session expiry: %w", err)
	}
	return Token{
		Bearer:         wire.Session.Bearer,
		ExpiresAt:      expiresAt,
		OrganizationID: wire.User.OrganizationID,
		Role:           wire.User.Role,
	}, nil
}
