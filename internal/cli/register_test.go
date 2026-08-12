package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runRegisterCapture runs register with controlled IO and returns the
// captured stdout and stderr text.
func runRegisterCapture(t *testing.T, args []string, stdin string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	err = runRegisterWithIO(args, &outBuf, &errBuf, strings.NewReader(stdin))
	return outBuf.String(), errBuf.String(), err
}

func TestRegisterMissingEmailIsUsageError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	_, _, err := runRegisterCapture(t, []string{
		"--organization-name", "Acme",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "hunter2secret\n")
	if err == nil {
		t.Fatal("register with no --email returned nil error")
	}
	if !strings.Contains(err.Error(), "--email is required") {
		t.Fatalf("register error = %v, want an --email is required message", err)
	}
	if networkHit {
		t.Fatal("register with no --email must not touch the network")
	}
}

func TestRegisterMissingOrganizationNameIsUsageError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	_, _, err := runRegisterCapture(t, []string{
		"--email", "user@example.com",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "hunter2secret\n")
	if err == nil {
		t.Fatal("register with no --organization-name returned nil error")
	}
	if !strings.Contains(err.Error(), "--organization-name is required") {
		t.Fatalf("register error = %v, want an --organization-name is required message", err)
	}
	if networkHit {
		t.Fatal("register with no --organization-name must not touch the network")
	}
}

func TestRegisterWhitespaceOnlyOrganizationNameIsUsageError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	_, _, err := runRegisterCapture(t, []string{
		"--email", "user@example.com",
		"--organization-name", "   ",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "hunter2secret\n")
	if err == nil {
		t.Fatal("register with a whitespace-only --organization-name returned nil error")
	}
	if !strings.Contains(err.Error(), "--organization-name is required") {
		t.Fatalf("register error = %v, want an --organization-name is required message", err)
	}
	if networkHit {
		t.Fatal("register with a whitespace-only --organization-name must not touch the network")
	}
}

func TestRegisterRejectsPlainPasswordFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	_, _, err := runRegisterCapture(t, []string{
		"--email", "user@example.com",
		"--organization-name", "Acme",
		"--password=hunter2secret",
		"--server-url", srv.URL,
	}, "")
	if err == nil {
		t.Fatal("register with --password=... returned nil error")
	}
	if !strings.Contains(err.Error(), "--password-stdin") {
		t.Fatalf("register error = %v, want a message pointing at --password-stdin", err)
	}
	if networkHit {
		t.Fatal("register with --password=... must not touch the network")
	}
}

func TestRegisterRejectsBarePasswordFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runRegisterCapture(t, []string{
		"--email", "user@example.com",
		"--organization-name", "Acme",
		"--password", "hunter2secret",
	}, "")
	if err == nil {
		t.Fatal("register with --password returned nil error")
	}
	if !strings.Contains(err.Error(), "--password-stdin") {
		t.Fatalf("register error = %v, want a message pointing at --password-stdin", err)
	}
}

func TestRegisterShortPasswordIsRejectedLocally(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	_, _, err := runRegisterCapture(t, []string{
		"--email", "user@example.com",
		"--organization-name", "Acme",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "short\n")
	if err == nil {
		t.Fatal("register with a too-short password returned nil error")
	}
	if !strings.Contains(err.Error(), "at least 12 bytes") {
		t.Fatalf("register error = %v, want a message about the 12-byte minimum", err)
	}
	if networkHit {
		t.Fatal("register with a too-short password must not touch the network")
	}
}

func TestRegisterSucceedsAndNeverPrintsSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var gotEmail, gotPassword, gotOrgName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email            string `json:"email"`
			Password         string `json:"password"`
			OrganizationName string `json:"organization_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotEmail = body.Email
		gotPassword = body.Password
		gotOrgName = body.OrganizationName
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"verification_pending"}`))
	}))
	defer srv.Close()

	stdout, _, err := runRegisterCapture(t, []string{
		"--email", "user@example.com",
		"--organization-name", "Acme",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "hunter2secret\n")
	if err != nil {
		t.Fatalf("register error = %v", err)
	}
	if gotEmail != "user@example.com" {
		t.Fatalf("server saw email = %q, want user@example.com", gotEmail)
	}
	if gotPassword != "hunter2secret" {
		t.Fatalf("server saw password = %q, want hunter2secret", gotPassword)
	}
	if gotOrgName != "Acme" {
		t.Fatalf("server saw organization_name = %q, want Acme", gotOrgName)
	}
	if !strings.Contains(stdout, "user@example.com") {
		t.Fatalf("stdout = %q, want it to include the email", stdout)
	}
	if strings.Contains(stdout, "hunter2secret") {
		t.Fatalf("stdout leaks the password: %q", stdout)
	}
}

func TestRegisterConflictReturnsTailoredMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	_, _, err := runRegisterCapture(t, []string{
		"--email", "user@example.com",
		"--organization-name", "Acme",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "hunter2secret\n")
	if err == nil {
		t.Fatal("register against a 409 server returned nil error")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("register error = %v, want an already registered message", err)
	}
}

func TestRegisterRateLimitedReturnsTailoredMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, _, err := runRegisterCapture(t, []string{
		"--email", "user@example.com",
		"--organization-name", "Acme",
		"--password-stdin",
		"--server-url", srv.URL,
	}, "hunter2secret\n")
	if err == nil {
		t.Fatal("register against a 429 server returned nil error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("register error = %v, want a rate limited message", err)
	}
}

func TestRegisterExplicitEmptyServerURLIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runRegisterCapture(t, []string{
		"--email", "user@example.com",
		"--organization-name", "Acme",
		"--password-stdin",
		"--server-url=",
	}, "hunter2secret\n")
	if err == nil {
		t.Fatal("register with an explicit empty --server-url returned nil error")
	}
	if !strings.Contains(err.Error(), "--server-url must not be empty") {
		t.Fatalf("register error = %v, want a --server-url must not be empty message", err)
	}
}

func TestRegisterUnknownFlagIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runRegisterCapture(t, []string{
		"--email", "user@example.com",
		"--organization-name", "Acme",
		"--password-stdin",
		"--bogus",
	}, "hunter2secret\n")
	if err == nil {
		t.Fatal("register with an unknown flag returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("register error = %v, want an unknown flag message", err)
	}
}
