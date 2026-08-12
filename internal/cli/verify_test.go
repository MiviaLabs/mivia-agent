package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

const verifyTestResponseBody = `{
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
    "bearer": "super-secret-bearer-value",
    "expires_at": "2026-08-13T18:00:00Z"
  }
}`

// runVerifyCapture runs verify with controlled IO and returns the captured
// stdout and stderr text.
func runVerifyCapture(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	err = runVerifyWithIO(args, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), err
}

func TestVerifyMissingCodeIsUsageError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := runVerifyCapture(t, []string{"--server-url", srv.URL})
	if err == nil {
		t.Fatal("verify with no code returned nil error")
	}
	if !strings.Contains(err.Error(), "a verification code is required") {
		t.Fatalf("verify error = %v, want a code-required message", err)
	}
	if networkHit {
		t.Fatal("verify with no code must not touch the network")
	}
}

func TestVerifyWhitespaceOnlyCodeIsUsageError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := runVerifyCapture(t, []string{"   ", "--server-url", srv.URL})
	if err == nil {
		t.Fatal("verify with a whitespace-only code returned nil error")
	}
	if !strings.Contains(err.Error(), "a verification code is required") {
		t.Fatalf("verify error = %v, want a code-required message", err)
	}
	if networkHit {
		t.Fatal("verify with a whitespace-only code must not touch the network")
	}
}

func TestVerifySucceedsPersistsTokenAndTrimsCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var gotToken, gotTransport string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotToken = body.Token
		gotTransport = r.Header.Get("X-Mivia-Auth-Transport")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(verifyTestResponseBody))
	}))
	defer srv.Close()

	stdout, _, err := runVerifyCapture(t, []string{"  abc123  ", "--server-url", srv.URL})
	if err != nil {
		t.Fatalf("verify error = %v", err)
	}
	if gotToken != "abc123" {
		t.Fatalf("server saw token = %q, want abc123 (trimmed)", gotToken)
	}
	if gotTransport != "cli" {
		t.Fatalf("server saw X-Mivia-Auth-Transport = %q, want cli", gotTransport)
	}
	if !strings.Contains(stdout, "Email verified") {
		t.Fatalf("stdout = %q, want a verified confirmation", stdout)
	}
	if strings.Contains(stdout, "super-secret-bearer-value") {
		t.Fatalf("stdout leaks the bearer token: %q", stdout)
	}

	tok, err := miviaauth.Load(config.UserAuthPath())
	if err != nil {
		t.Fatalf("Load() after verify error = %v", err)
	}
	if tok.Bearer != "super-secret-bearer-value" {
		t.Fatalf("persisted bearer = %q, want super-secret-bearer-value", tok.Bearer)
	}
}

func TestVerifySessionlessSuccessPrintsLoginHintAndPersistsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"authenticated":false,"status":"verified"}`))
	}))
	defer srv.Close()

	stdout, _, err := runVerifyCapture(t, []string{"abc123", "--server-url", srv.URL})
	if err != nil {
		t.Fatalf("verify error = %v, want nil (sessionless success)", err)
	}
	if !strings.Contains(stdout, "mivia login") {
		t.Fatalf("stdout = %q, want a `mivia login` hint", stdout)
	}

	authPath := config.UserAuthPath()
	if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected no auth file at %s after sessionless verify, stat err = %v", authPath, statErr)
	}
	_ = filepath.Join(home, ".mivia")
}

func TestVerifyInvalidCodeReturnsHonestMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	_, _, err := runVerifyCapture(t, []string{"abc123", "--server-url", srv.URL})
	if err == nil {
		t.Fatal("verify against a 400 server returned nil error")
	}
	if !strings.Contains(err.Error(), "invalid, expired, or already used") {
		t.Fatalf("verify error = %v, want the honest no-resend message", err)
	}
}

func TestVerifyExplicitEmptyServerURLIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runVerifyCapture(t, []string{"abc123", "--server-url="})
	if err == nil {
		t.Fatal("verify with an explicit empty --server-url returned nil error")
	}
	if !strings.Contains(err.Error(), "--server-url must not be empty") {
		t.Fatalf("verify error = %v, want a --server-url must not be empty message", err)
	}
}

func TestVerifyUnknownFlagIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runVerifyCapture(t, []string{"abc123", "--bogus"})
	if err == nil {
		t.Fatal("verify with an unknown flag returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("verify error = %v, want an unknown flag message", err)
	}
}
