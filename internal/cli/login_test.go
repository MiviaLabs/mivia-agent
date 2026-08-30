package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// loginTestResponseBody is a POST /v1/auth/login success body, in the shape
// apps/api's LoginResponseDto declares.
const loginTestResponseBody = `{
  "ok": true,
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.super-secret-bearer-value.sig",
  "refreshToken": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
  "user": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "email": "user@example.com",
    "organizationId": "org-42",
    "role": "member"
  },
  "expiresAt": "2030-08-13T18:00:00.000Z"
}`

// runLoginCapture runs login with controlled IO and returns the captured
// stdout and stderr text.
func runLoginCapture(t *testing.T, args []string, stdin string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	err = runLoginWithIO(args, &outBuf, &errBuf, strings.NewReader(stdin))
	return outBuf.String(), errBuf.String(), err
}

func TestLoginMissingEmailIsUsageError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := runLoginCapture(t, []string{
		"--password-stdin",
		"--server-url", srv.URL,
	}, "hunter2\n")
	if err == nil {
		t.Fatal("login with no --email returned nil error")
	}
	if !strings.Contains(err.Error(), "--email is required") {
		t.Fatalf("login error = %v, want an --email is required message", err)
	}
	if networkHit {
		t.Fatal("login with no --email must not touch the network")
	}
}

func TestLoginEmptyEmailIsUsageError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runLoginCapture(t, []string{
		"--email", "",
		"--password-stdin",
	}, "hunter2\n")
	if err == nil {
		t.Fatal("login with an empty --email returned nil error")
	}
	if !strings.Contains(err.Error(), "--email is required") {
		t.Fatalf("login error = %v, want an --email is required message", err)
	}
}

func TestLoginRejectsPlainPasswordFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := runLoginCapture(t, []string{
		"--email", "user@example.com",
		"--password=hunter2",
		"--server-url", srv.URL,
	}, "")
	if err == nil {
		t.Fatal("login with --password=... returned nil error")
	}
	if !strings.Contains(err.Error(), "--password-stdin") {
		t.Fatalf("login error = %v, want a message pointing at --password-stdin", err)
	}
	if networkHit {
		t.Fatal("login with --password=... must not touch the network")
	}
}

func TestLoginRejectsBarePasswordFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runLoginCapture(t, []string{
		"--email", "user@example.com",
		"--password", "hunter2",
	}, "")
	if err == nil {
		t.Fatal("login with --password returned nil error")
	}
	if !strings.Contains(err.Error(), "--password-stdin") {
		t.Fatalf("login error = %v, want a message pointing at --password-stdin", err)
	}
}

func TestLoginWithPasswordStdinSucceedsAndNeverPrintsSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var gotEmail, gotPassword string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotEmail = body.Email
		gotPassword = body.Password
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(loginTestResponseBody))
	}))
	defer srv.Close()

	stdout, _, err := runLoginCapture(t, []string{
		"--email", "user@example.com",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "hunter2\n")
	if err != nil {
		t.Fatalf("login error = %v", err)
	}
	if gotEmail != "user@example.com" {
		t.Fatalf("server saw email = %q, want user@example.com", gotEmail)
	}
	if gotPassword != "hunter2" {
		t.Fatalf("server saw password = %q, want hunter2", gotPassword)
	}
	if !strings.Contains(stdout, "Logged in as user@example.com.") {
		t.Fatalf("stdout = %q, want a login confirmation", stdout)
	}
	if strings.Contains(stdout, "hunter2") {
		t.Fatalf("stdout leaks the password: %q", stdout)
	}
	if strings.Contains(stdout, "super-secret-bearer-value") {
		t.Fatalf("stdout leaks the bearer token: %q", stdout)
	}
}

func TestLoginPasswordStdinTrimsTrailingNewline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var gotPassword string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPassword = body.Password
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(loginTestResponseBody))
	}))
	defer srv.Close()

	_, _, err := runLoginCapture(t, []string{
		"--email", "user@example.com",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "hunter2")
	if err != nil {
		t.Fatalf("login error = %v", err)
	}
	if gotPassword != "hunter2" {
		t.Fatalf("password = %q, want hunter2 (no trailing newline)", gotPassword)
	}
}

// TestLoginServerRejectsCredentialsReturnsError covers the only 401 the
// server produces for a failed login. It is deliberately identical for an
// unknown address and a wrong password -- the API refuses to leak which
// accounts exist -- so the message must cover both without claiming to know,
// and must point at the web app, since the CLI cannot create an account.
func TestLoginServerRejectsCredentialsReturnsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"statusCode":401,"error":"Unauthorized","message":"Invalid email or password"}`))
	}))
	defer srv.Close()

	stdout, _, err := runLoginCapture(t, []string{
		"--email", "user@example.com",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "wrong-password\n")
	if err == nil {
		t.Fatal("login against a 401 server returned nil error")
	}
	if !strings.Contains(err.Error(), webAppURL) {
		t.Errorf("error = %q, want it to point at %s for sign-up", err, webAppURL)
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("error = %q, want it to mention the password too; the server cannot tell the two cases apart", err)
	}
	if strings.Contains(stdout, "Logged in as") {
		t.Fatalf("stdout printed a confirmation despite the 401: %q", stdout)
	}
}

// TestLoginBadRequestSurfacesTheServerDetail: the API validates the email
// shape and password length, and its explanation is more useful than a bare
// "status 400". The CLI does not duplicate those rules, it quotes them.
func TestLoginBadRequestSurfacesTheServerDetail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"statusCode":400,"error":"Bad Request","message":["email must be an email"]}`))
	}))
	defer srv.Close()

	_, _, err := runLoginCapture(t, []string{
		"--email", "bob",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "hunter2hunter2\n")
	if err == nil {
		t.Fatal("login against a 400 server returned nil error")
	}
	if !strings.Contains(err.Error(), "email must be an email") {
		t.Errorf("error = %q, want the server's own explanation quoted", err)
	}
}

func TestLoginWithoutTTYOrPasswordStdinIsUsageError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := runLoginCapture(t, []string{
		"--email", "user@example.com",
		"--server-url", srv.URL,
	}, "")
	if err == nil {
		t.Fatal("login with no TTY and no --password-stdin returned nil error")
	}
	if !strings.Contains(err.Error(), "--password-stdin") {
		t.Fatalf("login error = %v, want a message pointing at --password-stdin", err)
	}
	if networkHit {
		t.Fatal("login with no password source must not touch the network")
	}
}

func TestLoginUnknownFlagIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runLoginCapture(t, []string{
		"--email", "user@example.com",
		"--password-stdin",
		"--bogus",
	}, "hunter2\n")
	if err == nil {
		t.Fatal("login with an unknown flag returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("login error = %v, want an unknown flag message", err)
	}
}

func TestLoginUnexpectedPositionalArgumentIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runLoginCapture(t, []string{
		"--email", "user@example.com",
		"--password-stdin",
		"unexpected-positional-arg",
	}, "hunter2\n")
	if err == nil {
		t.Fatal("login with an unexpected positional argument returned nil error")
	}
	if !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("login error = %v, want an unexpected argument message", err)
	}
}

func TestLoginExplicitEmptyServerURLIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runLoginCapture(t, []string{
		"--email", "user@example.com",
		"--password-stdin",
		"--server-url=",
	}, "hunter2\n")
	if err == nil {
		t.Fatal("login with an explicit empty --server-url returned nil error")
	}
	if !strings.Contains(err.Error(), "--server-url must not be empty") {
		t.Fatalf("login error = %v, want a --server-url must not be empty message", err)
	}
}
