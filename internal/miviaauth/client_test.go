package miviaauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const loginResponseBody = `{
  "authenticated": true,
  "user": {
    "account_id": "acct-1",
    "organization_id": "org-42",
    "organization_key": "org-key",
    "organization_name": "Acme",
    "role": "owner",
    "email": "user@example.com",
    "is_platform_super_admin": false,
    "name": "First",
    "lastname": "Last",
    "display_name": "First Last"
  },
  "session": {
    "bearer": "bearer-abc",
    "expires_at": "2026-08-13T18:00:00Z"
  }
}`

func wantExpiresAt(t *testing.T) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, "2026-08-13T18:00:00Z")
	if err != nil {
		t.Fatalf("time.Parse() error = %v", err)
	}
	return ts
}

func TestLoginSuccessParsesTokenAndSendsHeaders(t *testing.T) {
	var gotPath, gotTransport, gotMethod string
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotTransport = r.Header.Get("X-Mivia-Auth-Transport")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(loginResponseBody))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	tok, err := c.Login(context.Background(), "user@example.com", []byte("hunter2"))
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v2/auth/login" {
		t.Errorf("path = %q, want /api/v2/auth/login", gotPath)
	}
	if gotTransport != "cli" {
		t.Errorf("X-Mivia-Auth-Transport = %q, want cli", gotTransport)
	}
	if gotBody["email"] != "user@example.com" || gotBody["password"] != "hunter2" {
		t.Errorf("body = %+v, want email/password fields", gotBody)
	}

	want := Token{
		Bearer:         "bearer-abc",
		ExpiresAt:      wantExpiresAt(t),
		OrganizationID: "org-42",
		Role:           "owner",
	}
	if tok.Bearer != want.Bearer || !tok.ExpiresAt.Equal(want.ExpiresAt) ||
		tok.OrganizationID != want.OrganizationID || tok.Role != want.Role {
		t.Errorf("Login() = %+v, want %+v", tok, want)
	}
}

func TestLoginNon200ReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = c.Login(context.Background(), "user@example.com", []byte("bad"))
	if err == nil {
		t.Fatal("Login() error = nil, want a StatusError")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Login() error = %v, want *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want %d", statusErr.StatusCode, http.StatusUnauthorized)
	}
}

func TestRefreshSendsBearerAndNoBodyAndParses(t *testing.T) {
	var gotAuth, gotPath string
	var gotBodyLen int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, 1)
		n, _ := r.Body.Read(buf)
		gotBodyLen = n
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(loginResponseBody))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	tok, err := c.Refresh(context.Background(), "old-bearer")
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if gotPath != "/api/v2/auth/refresh" {
		t.Errorf("path = %q, want /api/v2/auth/refresh", gotPath)
	}
	if gotAuth != "Bearer old-bearer" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer old-bearer")
	}
	if gotBodyLen != 0 {
		t.Errorf("request body length = %d, want 0", gotBodyLen)
	}
	if tok.Bearer != "bearer-abc" {
		t.Errorf("Bearer = %q, want bearer-abc", tok.Bearer)
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

	_, err = c.Refresh(context.Background(), "old-bearer")
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Refresh() error = %v, want *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", statusErr.StatusCode)
	}
}

func TestRevokeSuccessReturnsNil(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if err := c.Revoke(context.Background(), "some-bearer"); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if gotPath != "/api/v2/auth/revoke" {
		t.Errorf("path = %q, want /api/v2/auth/revoke", gotPath)
	}
	if gotAuth != "Bearer some-bearer" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer some-bearer")
	}
}

func TestRevokeNon204ReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	err = c.Revoke(context.Background(), "some-bearer")
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Revoke() error = %v, want *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", statusErr.StatusCode)
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

func TestNewClientIgnoresAllowInsecureHTTPEnvVar(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")

	if _, err := NewClient("http://example.com"); err == nil {
		t.Fatal("NewClient() error = nil, want rejection despite MIVIA_ALLOW_INSECURE_HTTP=1")
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
