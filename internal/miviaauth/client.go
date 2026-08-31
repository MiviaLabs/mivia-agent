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
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// requestTimeout bounds a single login/refresh/revoke/me call. It is
// deliberately short: this is an interactive credential exchange, never a
// long-running stream. It is also the unit the refresh lock's budget is
// derived from (see service.go).
const requestTimeout = 10 * time.Second

// errorBodyLimit caps how much of a non-2xx body is read while classifying
// the failure. A body larger than this fails to decode, which leaves
// StatusError.FromAPI false -- the conservative direction, since FromAPI is
// what authorizes destroying a stored session.
const errorBodyLimit = 8 << 10

// detailLimit caps the server-supplied explanation carried on a StatusError.
const detailLimit = 400

// StatusError reports a non-2xx HTTP response from the mivia API's auth
// endpoints. Callers use errors.As to classify failures without parsing the
// error envelope themselves.
type StatusError struct {
	StatusCode int

	// FromAPI reports that the response body was the mivia API's own error
	// envelope, with a statusCode matching the HTTP status.
	//
	// This is the gate on destroying a stored session, and the reason it
	// exists: a captive portal, a corporate proxy, or an SSO interstitial can
	// answer any request with a bare 401 and an HTML body. Under the old
	// bearer-only contract the collateral for believing one was a 1-hour
	// token that was expiring anyway. Under this contract it is a 30-day
	// refresh token that is still perfectly valid server-side, so a 401 is
	// only definitive when the API demonstrably sent it.
	FromAPI bool

	// Detail is the envelope's message, sanitized for terminal output. Empty
	// when the response was not the API's envelope.
	Detail string
}

func (e *StatusError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("miviaauth: server responded with status %d: %s", e.StatusCode, e.Detail)
	}
	return fmt.Sprintf("miviaauth: server responded with status %d", e.StatusCode)
}

// Identity is the authenticated user reported by GET /v1/auth/me.
type Identity struct {
	ID             string
	Email          string
	OrganizationID string
	Role           string

	// DisplayName is empty when the server sent null, which the schema
	// allows.
	DisplayName string
}

// Client talks to the mivia API's /v1/auth endpoints.
type Client struct {
	baseURL string
	http    *http.Client
}

// Pins *Client to Service's expected interface at compile time, so a
// future signature drift here is caught immediately instead of at the
// point Service is first wired to a real Client.
var _ sessionClient = (*Client)(nil)

// NewClient validates baseURL and returns a Client. baseURL must be an
// absolute https URL, with one exception: a loopback address (127.0.0.1,
// ::1, or the literal "localhost") over plain http is accepted for local
// dev. This exception does NOT consult MIVIA_ALLOW_INSECURE_HTTP — that
// env var is scoped to credential-free LLM provider traffic; this endpoint
// carries a password and gets its own, narrower exception with no env
// override.
//
// baseURL is the API root only. The /v1 version prefix belongs to the
// request paths, not to the configured server, so pointing
// MIVIA_API_BASE_URL at a local API needs no version in the value.
func NewClient(baseURL string) (*Client, error) {
	if path, segment, versioned := versionedAPIPath(baseURL); versioned {
		return nil, fmt.Errorf(
			"miviaauth: base url must be the API root, but %q ends in the version segment %q: requests would go to %s/v1/auth/login. Remove the trailing /%s",
			baseURL, segment, path, segment)
	}
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
	var out sessionResponse
	err := c.do(ctx, http.MethodPost, "/v1/auth/login", "",
		loginRequest{Email: email, Password: string(password)}, &out)
	if err != nil {
		return Token{}, err
	}
	return tokenFromSession(out)
}

// Refresh exchanges the stored refresh token for a new session.
//
// The refresh token is one-time use: the server rotates it and the value
// passed here is dead the moment this call succeeds. The returned Token
// carries the replacement, and losing it loses the session -- see
// Service.refresh.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	var out sessionResponse
	err := c.do(ctx, http.MethodPost, "/v1/auth/refresh", "",
		refreshRequest{RefreshToken: refreshToken}, &out)
	if err != nil {
		return Token{}, err
	}
	return tokenFromSession(out)
}

// Revoke ends the session server-side. It is authenticated: bearer must be a
// live access token, and the endpoint falls back to the session named by that
// token's own jti claim when refreshToken is empty. Revoking an
// already-revoked session succeeds.
func (c *Client) Revoke(ctx context.Context, bearer, refreshToken string) error {
	var out okResponse
	if err := c.do(ctx, http.MethodPost, "/v1/auth/revoke", bearer,
		revokeRequest{RefreshToken: refreshToken}, &out); err != nil {
		return err
	}
	if !out.OK {
		// A 2xx without the flag is not this API answering. An intercepting
		// proxy that returns 200 for everything would otherwise be reported
		// as a successful revocation.
		return fmt.Errorf("miviaauth: revoke: response was not the API's acknowledgement")
	}
	return nil
}

// Me reports the authenticated identity behind bearer.
func (c *Client) Me(ctx context.Context, bearer string) (Identity, error) {
	var out meResponse
	if err := c.do(ctx, http.MethodGet, "/v1/auth/me", bearer, nil, &out); err != nil {
		return Identity{}, err
	}
	id := Identity{
		ID:             out.ID,
		Email:          out.Email,
		OrganizationID: out.OrganizationID,
		Role:           out.Role,
	}
	if out.DisplayName != nil {
		id.DisplayName = *out.DisplayName
	}
	return id, nil
}

// tokenFromSession converts a login or refresh response into a Token, and is
// the single place an incomplete success response is refused.
//
// This guard is load-bearing beyond tidiness: Service treats an empty
// RefreshToken on disk as a pre-/v1 session file and clears it. Without this
// check a 2xx carrying `{}` would be saved as a Token with no refresh token,
// login would report success, and every later command would claim the user
// was never logged in.
func tokenFromSession(resp sessionResponse) (Token, error) {
	switch {
	case resp.Token == "":
		return Token{}, fmt.Errorf("miviaauth: response carried no access token")
	case resp.RefreshToken == "":
		return Token{}, fmt.Errorf("miviaauth: response carried no refresh token")
	case resp.ExpiresAt.IsZero():
		return Token{}, fmt.Errorf("miviaauth: response carried no expiry")
	}
	return Token{
		Bearer:         resp.Token,
		RefreshToken:   resp.RefreshToken,
		ExpiresAt:      resp.ExpiresAt,
		OrganizationID: resp.User.OrganizationID,
		Role:           resp.User.Role,
	}, nil
}

// do performs one request against path and decodes a 2xx body into out.
//
// Any 2xx is a success. The API declares 200 on all four routes, but that is
// read from decorators and has never been observed on the wire from this
// repository or the API's own tests, so a route that ships a 201 or 204
// instead must not break the client.
//
// Non-2xx responses become a *StatusError; network-level failures (including
// context cancellation) stay plain wrapped errors, so callers can tell "the
// server said no" from "we never reached the server".
func (c *Client) do(ctx context.Context, method, path, bearer string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("miviaauth: encode %s request: %w", path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("miviaauth: build %s request: %w", path, err)
	}
	if body != nil {
		// Required, not decorative: without it the server's body parser
		// leaves the request body empty and the ValidationPipe rejects the
		// call as missing every required field.
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("miviaauth: request failed: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("miviaauth: request failed: %w", statusErrorFrom(resp))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("miviaauth: decode %s response: %w", path, err)
	}
	return nil
}

// statusErrorFrom classifies a non-2xx response by reading a bounded prefix
// of its body and looking for the API's error envelope.
func statusErrorFrom(resp *http.Response) *StatusError {
	out := &StatusError{StatusCode: resp.StatusCode}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	if err != nil {
		return out
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return out
	}
	if envelope.StatusCode != resp.StatusCode {
		// Either not the API's envelope, or an envelope disagreeing with its
		// own transport status. Both are reasons not to trust it.
		return out
	}
	out.FromAPI = true
	out.Detail = sanitizeDetail(string(envelope.Message))
	return out
}

// sanitizeDetail prepares server-controlled text for a terminal. The message
// is quoted back to the user so a rejected login says which rule it broke,
// which means a hostile or merely buggy server gets to put bytes on a TTY:
// control characters and escape introducers are dropped rather than
// forwarded, and the result is length-capped.
func sanitizeDetail(msg string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\t' || r == ' ' {
			return ' '
		}
		if unicode.IsControl(r) || r == 0x7f {
			return -1
		}
		return r
	}, msg)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if len(cleaned) > detailLimit {
		cleaned = cleaned[:detailLimit] + "..."
	}
	return cleaned
}
