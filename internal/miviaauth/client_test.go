package miviaauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture reads a checked-in response body from testdata/contract. Tests
// serve these verbatim so an httptest handler cannot quietly invent a shape
// the real API never sends.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "contract", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

// capturedRequest records everything a test needs to assert about what the
// client put on the wire.
type capturedRequest struct {
	method        string
	path          string
	authorization string
	contentType   string
	headers       http.Header
	body          string
}

// serveOnce stands up a server that records the single request it receives
// and answers with status and body, and returns a Client pointed at it.
func serveOnce(t *testing.T, status int, body string) (*Client, *capturedRequest) {
	t.Helper()
	var got capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 0, 1024)
		buf := make([]byte, 512)
		for {
			n, err := r.Body.Read(buf)
			raw = append(raw, buf[:n]...)
			if err != nil {
				break
			}
		}
		got = capturedRequest{
			method:        r.Method,
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			contentType:   r.Header.Get("Content-Type"),
			headers:       r.Header.Clone(),
			body:          string(raw),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient(%q) error = %v", srv.URL, err)
	}
	return client, &got
}

// routeRecorder adapts serveOnce for the contract test in
// wire_contract_test.go, which drives every recorded route generically.
type routeRecorder struct {
	client *Client
	*capturedRequest
}

func newRouteRecorder(t *testing.T, route contractRoute) *routeRecorder {
	t.Helper()
	body := "{}"
	switch route.ResponseStruct {
	case "sessionResponse":
		body = fixture(t, "session_response.json")
	case "meResponse":
		body = fixture(t, "me_response.json")
	case "okResponse":
		body = fixture(t, "revoke_response.json")
	}
	client, got := serveOnce(t, route.SuccessStatus, body)
	return &routeRecorder{client: client, capturedRequest: got}
}

// call invokes the client method for a recorded route name. An unrecognized
// name is fatal rather than a silent skip: a route added to the contract
// without a client method must fail, not pass vacuously.
func (r *routeRecorder) call(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	var err error
	switch name {
	case "login":
		_, err = r.client.Login(ctx, "user@example.com", []byte("hunter2hunter2"))
	case "refresh":
		_, err = r.client.Refresh(ctx, "rt-0")
	case "revoke":
		err = r.client.Revoke(ctx, "bearer-0", "rt-0")
	case "me":
		_, err = r.client.Me(ctx, "bearer-0")
	default:
		t.Fatalf("contract records route %q but the test has no client call for it", name)
	}
	if err != nil {
		t.Fatalf("%s: unexpected error = %v", name, err)
	}
}

func TestRefresh401ReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = c.Refresh(context.Background(), "old-refresh-token")
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Refresh() error = %v, want *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", statusErr.StatusCode)
	}
	if statusErr.FromAPI {
		t.Error("FromAPI = true for a bare 401 with no error envelope")
	}
}

func TestNewClientRejectsPlainHTTP(t *testing.T) {
	if _, err := NewClient("http://example.com"); err == nil {
		t.Fatal("NewClient() error = nil, want rejection of plain http URL")
	}
}

func TestNewClientAcceptsHTTPS(t *testing.T) {
	if _, err := NewClient("https://example.com"); err != nil {
		t.Fatalf("NewClient() error = %v, want nil", err)
	}
}

func TestNewClientAcceptsLoopbackHTTP(t *testing.T) {
	if _, err := NewClient("http://127.0.0.1:8090"); err != nil {
		t.Errorf("NewClient(127.0.0.1) error = %v, want nil", err)
	}
	if _, err := NewClient("http://localhost:8090"); err != nil {
		t.Errorf("NewClient(localhost) error = %v, want nil", err)
	}
}

// TestNewClientIgnoresAllowInsecureHTTPEnvVar keeps the narrow loopback
// exception narrow: MIVIA_ALLOW_INSECURE_HTTP is scoped to credential-free
// provider traffic and must never open this password-carrying endpoint.
func TestNewClientIgnoresAllowInsecureHTTPEnvVar(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")

	if _, err := NewClient("http://example.com"); err == nil {
		t.Fatal("NewClient() error = nil, want rejection despite MIVIA_ALLOW_INSECURE_HTTP=1")
	}
}

func TestLoginPostsCredentialsAndParsesTheFlatSession(t *testing.T) {
	client, got := serveOnce(t, http.StatusOK, fixture(t, "session_response.json"))

	tok, err := client.Login(context.Background(), "user@example.com", []byte("hunter2hunter2"))
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if got.path != "/v1/auth/login" || got.method != http.MethodPost {
		t.Errorf("sent %s %s, want POST /v1/auth/login", got.method, got.path)
	}
	var sent loginRequest
	if err := json.Unmarshal([]byte(got.body), &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.Email != "user@example.com" || sent.Password != "hunter2hunter2" {
		t.Errorf("sent body = %+v, want the supplied credentials", sent)
	}

	if tok.Bearer == "" || !strings.HasPrefix(tok.Bearer, "eyJ") {
		t.Errorf("Bearer = %q, want the response's JWT", tok.Bearer)
	}
	if tok.RefreshToken != "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" {
		t.Errorf("RefreshToken = %q, want the response's refresh token", tok.RefreshToken)
	}
	if tok.OrganizationID != "550e8400-e29b-41d4-a716-446655440001" {
		t.Errorf("OrganizationID = %q, want the nested user's organizationId", tok.OrganizationID)
	}
	if tok.Role != "member" {
		t.Errorf("Role = %q, want the nested user's role", tok.Role)
	}
	want := time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC)
	if !tok.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v parsed from the ISO-8601 string", tok.ExpiresAt, want)
	}
}

// TestRequestHeadersAreExactlyContentTypeAndAuthorization pins both halves of
// a deliberate change: the go-mivia-era X-Mivia-Auth-Transport header is gone
// (it is not part of this contract), and Content-Type stays (without it the
// server's body parser yields an empty body and the ValidationPipe rejects
// the call as missing every field).
func TestRequestHeadersAreExactlyContentTypeAndAuthorization(t *testing.T) {
	t.Run("public post sends content-type and no authorization", func(t *testing.T) {
		client, got := serveOnce(t, http.StatusOK, fixture(t, "session_response.json"))
		if _, err := client.Refresh(context.Background(), "rt-0"); err != nil {
			t.Fatalf("Refresh() error = %v", err)
		}
		if got.contentType != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got.contentType)
		}
		if got.authorization != "" {
			t.Errorf("Authorization = %q, want none on a public route", got.authorization)
		}
		if v := got.headers.Get("X-Mivia-Auth-Transport"); v != "" {
			t.Errorf("X-Mivia-Auth-Transport = %q, want it gone from this contract", v)
		}
	})

	t.Run("authenticated get sends authorization and no content-type", func(t *testing.T) {
		client, got := serveOnce(t, http.StatusOK, fixture(t, "me_response.json"))
		if _, err := client.Me(context.Background(), "bearer-0"); err != nil {
			t.Fatalf("Me() error = %v", err)
		}
		if got.authorization != "Bearer bearer-0" {
			t.Errorf("Authorization = %q, want the bearer", got.authorization)
		}
		if got.contentType != "" {
			t.Errorf("Content-Type = %q, want none on a bodiless GET", got.contentType)
		}
		if v := got.headers.Get("X-Mivia-Auth-Transport"); v != "" {
			t.Errorf("X-Mivia-Auth-Transport = %q, want it gone from this contract", v)
		}
	})
}

// TestRefreshSendsTheRefreshTokenInTheBodyNotAsABearer pins the shape change
// from the old contract, where refresh authenticated with the bearer it was
// replacing.
func TestRefreshSendsTheRefreshTokenInTheBodyNotAsABearer(t *testing.T) {
	client, got := serveOnce(t, http.StatusOK, fixture(t, "session_response.json"))

	tok, err := client.Refresh(context.Background(), "rt-old")
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got.authorization != "" {
		t.Errorf("Authorization = %q, want refresh to authenticate by body alone", got.authorization)
	}
	var sent refreshRequest
	if err := json.Unmarshal([]byte(got.body), &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.RefreshToken != "rt-old" {
		t.Errorf("sent refreshToken = %q, want the stored one", sent.RefreshToken)
	}
	// The rotation contract: what comes back must not be what went out.
	if tok.RefreshToken == "rt-old" {
		t.Error("Refresh returned the token it was given; the server rotates it")
	}
}

func TestRevokeSendsBearerAndRefreshTokenAndAcceptsAny2xx(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client, got := serveOnce(t, status, fixture(t, "revoke_response.json"))
			if err := client.Revoke(context.Background(), "bearer-0", "rt-0"); err != nil {
				t.Fatalf("Revoke() error = %v", err)
			}
			if got.path != "/v1/auth/revoke" || got.method != http.MethodPost {
				t.Errorf("sent %s %s, want POST /v1/auth/revoke", got.method, got.path)
			}
			if got.authorization != "Bearer bearer-0" {
				t.Errorf("Authorization = %q, want the bearer; revoke is authenticated", got.authorization)
			}
			var sent revokeRequest
			if err := json.Unmarshal([]byte(got.body), &sent); err != nil {
				t.Fatalf("decode sent body: %v", err)
			}
			if sent.RefreshToken != "rt-0" {
				t.Errorf("sent refreshToken = %q, want the stored one", sent.RefreshToken)
			}
		})
	}
}

// TestRevokeWithoutARefreshTokenStillSendsAValidBody covers logout with a
// legacy or missing refresh token: the empty string is what makes the server
// fall back to the session named by the caller's own jti claim, so the field
// must be present and empty, never omitted.
func TestRevokeWithoutARefreshTokenStillSendsAValidBody(t *testing.T) {
	client, got := serveOnce(t, http.StatusOK, fixture(t, "revoke_response.json"))
	if err := client.Revoke(context.Background(), "bearer-0", ""); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if !strings.Contains(got.body, `"refreshToken"`) {
		t.Errorf("body = %q, want the refreshToken key present and empty", got.body)
	}
}

// TestRevokeWithoutTheOkFlagIsAnError is the captive-portal guard: an
// intercepting proxy that answers 200 to everything must not read as a
// successful server-side revocation.
func TestRevokeWithoutTheOkFlagIsAnError(t *testing.T) {
	client, _ := serveOnce(t, http.StatusOK, `<html>Sign in to the hotel wifi</html>`)
	err := client.Revoke(context.Background(), "bearer-0", "rt-0")
	if err == nil {
		t.Fatal("Revoke() returned nil for a 200 that was not the API's acknowledgement")
	}
}

func TestMeParsesTheIdentity(t *testing.T) {
	client, got := serveOnce(t, http.StatusOK, fixture(t, "me_response.json"))

	id, err := client.Me(context.Background(), "bearer-0")
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if got.path != "/v1/auth/me" || got.method != http.MethodGet {
		t.Errorf("sent %s %s, want GET /v1/auth/me", got.method, got.path)
	}
	want := Identity{
		ID:             "550e8400-e29b-41d4-a716-446655440000",
		Email:          "user@example.com",
		OrganizationID: "550e8400-e29b-41d4-a716-446655440001",
		Role:           "member",
		DisplayName:    "Jane Doe",
	}
	if id != want {
		t.Errorf("Me() = %+v, want %+v", id, want)
	}
}

func TestMeNullDisplayNameDecodesAsEmpty(t *testing.T) {
	client, _ := serveOnce(t, http.StatusOK, fixture(t, "me_response_null_display_name.json"))
	id, err := client.Me(context.Background(), "bearer-0")
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if id.DisplayName != "" {
		t.Errorf("DisplayName = %q, want empty for a null", id.DisplayName)
	}
	if id.Email != "user@example.com" {
		t.Errorf("Email = %q, want the rest of the identity to survive the null", id.Email)
	}
}

// TestIncompleteSuccessResponseIsRejected is what makes an empty RefreshToken
// on disk a reliable marker of a pre-/v1 session file: no path through this
// client can save one.
func TestIncompleteSuccessResponseIsRejected(t *testing.T) {
	cases := map[string]string{
		"empty object":     `{"ok":true}`,
		"no refresh token": `{"ok":true,"token":"t","expiresAt":"2026-08-30T11:00:00.000Z","user":{}}`,
		"no access token":  `{"ok":true,"refreshToken":"r","expiresAt":"2026-08-30T11:00:00.000Z","user":{}}`,
		"no expiry":        `{"ok":true,"token":"t","refreshToken":"r","user":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			client, _ := serveOnce(t, http.StatusOK, body)
			if _, err := client.Login(context.Background(), "user@example.com", []byte("hunter2hunter2")); err == nil {
				t.Fatal("Login() accepted an incomplete 200")
			}
			client, _ = serveOnce(t, http.StatusOK, body)
			if _, err := client.Refresh(context.Background(), "rt-0"); err == nil {
				t.Fatal("Refresh() accepted an incomplete 200")
			}
		})
	}
}

// TestStatusErrorFromAPIOnlyWhenTheEnvelopeAgrees is the guard on destroying a
// still-valid 30-day session because something between the CLI and the API
// answered 401.
func TestStatusErrorFromAPIOnlyWhenTheEnvelopeAgrees(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		wantFromAPI bool
	}{
		{"api envelope", http.StatusUnauthorized, fixture(t, "error_envelope_401.json"), true},
		{"captive portal html", http.StatusUnauthorized, `<html>Sign in to continue</html>`, false},
		{"empty body", http.StatusUnauthorized, ``, false},
		{"json without a status code", http.StatusUnauthorized, `{"error":"nope"}`, false},
		{"envelope disagreeing with its transport status", http.StatusUnauthorized,
			`{"statusCode":403,"error":"Forbidden","message":"no"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := serveOnce(t, tc.status, tc.body)
			_, err := client.Login(context.Background(), "user@example.com", []byte("hunter2hunter2"))

			var statusErr *StatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("Login() error = %v, want a *StatusError", err)
			}
			if statusErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", statusErr.StatusCode, tc.status)
			}
			if statusErr.FromAPI != tc.wantFromAPI {
				t.Errorf("FromAPI = %v, want %v", statusErr.FromAPI, tc.wantFromAPI)
			}
		})
	}
}

// TestStatusErrorDetailComesFromTheResponseNotTheRequest asserts the quoted
// explanation is the server's, and that nothing the client sent leaks into
// it. The marker is a value only the server body carries.
func TestStatusErrorDetailComesFromTheResponseNotTheRequest(t *testing.T) {
	const marker = "server-authored-explanation-7f3a"
	body := `{"statusCode":400,"error":"Bad Request","message":"` + marker + `"}`
	client, _ := serveOnce(t, http.StatusBadRequest, body)

	_, err := client.Login(context.Background(), "someone@example.com", []byte("correct-horse-battery"))

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Login() error = %v, want a *StatusError", err)
	}
	if statusErr.Detail != marker {
		t.Errorf("Detail = %q, want the server's message %q", statusErr.Detail, marker)
	}
	for _, sent := range []string{"correct-horse-battery", "someone@example.com"} {
		if strings.Contains(err.Error(), sent) {
			t.Errorf("error text %q leaked a request value %q", err.Error(), sent)
		}
	}
}

func TestStatusErrorDetailJoinsAValidationMessageArray(t *testing.T) {
	client, _ := serveOnce(t, http.StatusBadRequest, fixture(t, "error_envelope_400_validation.json"))
	_, err := client.Login(context.Background(), "bob", []byte("short"))

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Login() error = %v, want a *StatusError", err)
	}
	for _, want := range []string{"email must be an email", "password must be longer"} {
		if !strings.Contains(statusErr.Detail, want) {
			t.Errorf("Detail = %q, want it to carry %q", statusErr.Detail, want)
		}
	}
}

// TestStatusErrorDetailStripsControlBytes covers a hostile or merely broken
// server: Detail is printed to a terminal, so escape introducers and control
// characters must not survive the trip.
func TestStatusErrorDetailStripsControlBytes(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"statusCode": 400,
		"error":      "Bad Request",
		"message":    "before\x1b[2Jafter\nsecond\x07line",
	})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := serveOnce(t, http.StatusBadRequest, string(body))
	_, reqErr := client.Login(context.Background(), "user@example.com", []byte("hunter2hunter2"))

	var statusErr *StatusError
	if !errors.As(reqErr, &statusErr) {
		t.Fatalf("Login() error = %v, want a *StatusError", reqErr)
	}
	for _, bad := range []string{"\x1b", "\n", "\x07"} {
		if strings.Contains(statusErr.Detail, bad) {
			t.Errorf("Detail = %q, still carries %q", statusErr.Detail, bad)
		}
	}
	if !strings.Contains(statusErr.Detail, "before") || !strings.Contains(statusErr.Detail, "after") {
		t.Errorf("Detail = %q, want the readable text preserved", statusErr.Detail)
	}
}

func TestLoginContextCancellationReturnsPromptly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = c.Login(ctx, "user@example.com", []byte("pw"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Login() error = nil, want a context deadline error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Login() took %v, want a prompt return after context timeout", elapsed)
	}
}
