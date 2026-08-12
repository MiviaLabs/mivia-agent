package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// runLogoutCapture runs logout with controlled IO and returns the captured
// stdout and stderr text.
func runLogoutCapture(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	err = runLogoutWithIO(args, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), err
}

func TestLogoutWithNothingStoredSucceedsAndSkipsNetwork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var revokeHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeHit = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	stdout, _, err := runLogoutCapture(t, []string{"--server-url", srv.URL})
	if err != nil {
		t.Fatalf("logout error = %v", err)
	}
	if !strings.Contains(stdout, "Logged out.") {
		t.Fatalf("stdout = %q, want a logout confirmation", stdout)
	}
	if revokeHit {
		t.Fatal("logout with nothing stored must not call the revoke endpoint (Service.Logout skips the network call)")
	}
}

func TestLogoutWithStoredTokenSucceedsDespiteRevoke500(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	authPath := config.UserAuthPath()
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := miviaauth.Save(authPath, miviaauth.Token{Bearer: "stored-bearer"}); err != nil {
		t.Fatal(err)
	}

	var revokeHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeHit = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	stdout, _, err := runLogoutCapture(t, []string{"--server-url", srv.URL})
	if err != nil {
		t.Fatalf("logout error = %v, want offline-safe success despite the 500", err)
	}
	if !strings.Contains(stdout, "Logged out.") {
		t.Fatalf("stdout = %q, want a logout confirmation", stdout)
	}
	if !revokeHit {
		t.Fatal("logout with a stored token should have attempted the revoke call")
	}
	if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
		t.Fatalf("token file still exists after logout (stat err=%v)", statErr)
	}
}

func TestLogoutWithStoredTokenAndRevoke204Succeeds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	authPath := config.UserAuthPath()
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := miviaauth.Save(authPath, miviaauth.Token{Bearer: "stored-bearer"}); err != nil {
		t.Fatal(err)
	}

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	stdout, _, err := runLogoutCapture(t, []string{"--server-url", srv.URL})
	if err != nil {
		t.Fatalf("logout error = %v", err)
	}
	if !strings.Contains(stdout, "Logged out.") {
		t.Fatalf("stdout = %q, want a logout confirmation", stdout)
	}
	if gotAuth != "Bearer stored-bearer" {
		t.Fatalf("revoke Authorization header = %q, want Bearer stored-bearer", gotAuth)
	}
	if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
		t.Fatalf("token file still exists after logout (stat err=%v)", statErr)
	}
}

func TestLogoutUnknownFlagIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runLogoutCapture(t, []string{"--bogus"})
	if err == nil {
		t.Fatal("logout with an unknown flag returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("logout error = %v, want an unknown flag message", err)
	}
}

func TestLogoutUnexpectedPositionalArgumentIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runLogoutCapture(t, []string{"unexpected-positional-arg"})
	if err == nil {
		t.Fatal("logout with an unexpected positional argument returned nil error")
	}
	if !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("logout error = %v, want an unexpected argument message", err)
	}
}

func TestLogoutExplicitEmptyServerURLIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runLogoutCapture(t, []string{"--server-url="})
	if err == nil {
		t.Fatal("logout with an explicit empty --server-url returned nil error")
	}
	if !strings.Contains(err.Error(), "--server-url must not be empty") {
		t.Fatalf("logout error = %v, want a --server-url must not be empty message", err)
	}
}
