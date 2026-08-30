package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// runWhoamiCapture runs whoami with controlled IO and returns the captured
// stdout and stderr text.
func runWhoamiCapture(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	err = runWhoamiWithIO(args, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), err
}

// storeSession writes a session file under the test's HOME.
func storeSession(t *testing.T, tok miviaauth.Token) string {
	t.Helper()
	authPath := config.UserAuthPath()
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := miviaauth.Save(authPath, tok); err != nil {
		t.Fatal(err)
	}
	return authPath
}

// meServer answers GET /v1/auth/me with body, and fails the test if whoami
// tries to refresh -- the callers below all store a token that is nowhere
// near expiry.
func meServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/me" {
			t.Errorf("unexpected request to %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWhoamiPrintsIdentityAndExpiry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	expires := time.Now().Add(42 * time.Minute)
	storeSession(t, miviaauth.Token{
		Bearer:       "stored-bearer",
		RefreshToken: "rt-1",
		ExpiresAt:    expires,
	})
	srv := meServer(t, http.StatusOK, `{
	  "id": "550e8400-e29b-41d4-a716-446655440000",
	  "email": "user@example.com",
	  "organizationId": "org-42",
	  "role": "member",
	  "displayName": "Jane Doe"
	}`)

	stdout, _, err := runWhoamiCapture(t, []string{"--server-url", srv.URL})
	if err != nil {
		t.Fatalf("whoami error = %v", err)
	}
	for _, want := range []string{"user@example.com", "org-42", "member", "Jane Doe"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
	if !strings.Contains(stdout, expires.Format(time.RFC3339)) {
		t.Errorf("stdout = %q, want the access-token expiry %s", stdout, expires.Format(time.RFC3339))
	}
	if !strings.Contains(stdout, "in 42m") {
		t.Errorf("stdout = %q, want a readable countdown", stdout)
	}
}

// TestWhoamiOmitsDisplayNameWhenNull: the schema allows a null display name,
// and a blank "Display name:" line would read as a bug rather than as absence.
func TestWhoamiOmitsDisplayNameWhenNull(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storeSession(t, miviaauth.Token{
		Bearer:       "stored-bearer",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	srv := meServer(t, http.StatusOK, `{
	  "id": "u1", "email": "user@example.com",
	  "organizationId": "org-42", "role": "member", "displayName": null
	}`)

	stdout, _, err := runWhoamiCapture(t, []string{"--server-url", srv.URL})
	if err != nil {
		t.Fatalf("whoami error = %v", err)
	}
	if strings.Contains(stdout, "Display name:") {
		t.Errorf("stdout = %q, want no display-name line when the server sent null", stdout)
	}
	if !strings.Contains(stdout, "user@example.com") {
		t.Errorf("stdout = %q, want the rest of the identity", stdout)
	}
}

func TestWhoamiNotLoggedInPointsAtLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
	}))
	defer srv.Close()

	_, _, err := runWhoamiCapture(t, []string{"--server-url", srv.URL})
	if err == nil {
		t.Fatal("whoami with no stored session returned nil error")
	}
	if !strings.Contains(err.Error(), "mivia login") {
		t.Errorf("error = %v, want it to point at `mivia login`", err)
	}
	if networkHit {
		t.Error("whoami called the server with nothing stored")
	}
}

// TestWhoamiRefreshesAnExpiredBearerBeforeReporting is the command-level view
// of the semantics this rewire fixes: an hour-old access token is normal, and
// the stored refresh token is what makes it recoverable without a login.
func TestWhoamiRefreshesAnExpiredBearerBeforeReporting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	authPath := storeSession(t, miviaauth.Token{
		Bearer:       "expired-bearer",
		RefreshToken: "rt-old",
		ExpiresAt:    time.Now().Add(-time.Hour),
	})

	var refreshed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/refresh":
			refreshed = true
			_, _ = w.Write([]byte(`{
			  "ok": true, "token": "fresh-bearer", "refreshToken": "rt-new",
			  "user": {"id":"u1","email":"user@example.com","organizationId":"org-42","role":"member"},
			  "expiresAt": "` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano) + `"
			}`))
		case "/v1/auth/me":
			if got := r.Header.Get("Authorization"); got != "Bearer fresh-bearer" {
				t.Errorf("me Authorization = %q, want the refreshed bearer", got)
			}
			_, _ = w.Write([]byte(`{"id":"u1","email":"user@example.com","organizationId":"org-42","role":"member","displayName":null}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	stdout, _, err := runWhoamiCapture(t, []string{"--server-url", srv.URL})
	if err != nil {
		t.Fatalf("whoami error = %v, want a silent refresh", err)
	}
	if !refreshed {
		t.Error("whoami did not refresh an expired bearer")
	}
	if !strings.Contains(stdout, "user@example.com") {
		t.Errorf("stdout = %q, want the identity", stdout)
	}
	tok, err := miviaauth.Load(authPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if tok.RefreshToken != "rt-new" {
		t.Errorf("stored RefreshToken = %q, want the rotated value persisted", tok.RefreshToken)
	}
}

// TestWhoamiWithALegacySessionFilePointsAtLogin covers upgrading over an
// auth.json from before the /v1 contract: no refresh token, and a bearer this
// API cannot authenticate. It must degrade to a clear instruction, never a
// crash and never a raw 401.
func TestWhoamiWithALegacySessionFilePointsAtLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	authPath := config.UserAuthPath()
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"Bearer":"go-mivia-era-bearer","ExpiresAt":"2030-01-01T00:00:00Z","OrganizationID":"org-1","Role":"admin"}`
	if err := os.WriteFile(authPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	var networkHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkHit = true
	}))
	defer srv.Close()

	_, _, err := runWhoamiCapture(t, []string{"--server-url", srv.URL})
	if err == nil {
		t.Fatal("whoami with a legacy session file returned nil error")
	}
	if !strings.Contains(err.Error(), "mivia login") {
		t.Errorf("error = %v, want it to point at `mivia login`", err)
	}
	if networkHit {
		t.Error("whoami sent a pre-/v1 bearer to the server; it cannot authenticate one")
	}
	if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
		t.Error("the unusable legacy session file was left in place")
	}
}

// TestWhoamiInterceptedMe401KeepsTheSession: whoami reaches /v1/auth/me with
// a bearer that has not otherwise touched the network, which makes it the
// likeliest place to meet a captive portal -- and the most expensive place to
// believe one, since the stored refresh token is good for 30 days.
func TestWhoamiInterceptedMe401KeepsTheSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	authPath := storeSession(t, miviaauth.Token{
		Bearer:       "stored-bearer",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	srv := meServer(t, http.StatusUnauthorized, `<html>Sign in to the hotel wifi</html>`)

	if _, _, err := runWhoamiCapture(t, []string{"--server-url", srv.URL}); err == nil {
		t.Fatal("whoami returned nil after a 401")
	}
	if _, statErr := os.Stat(authPath); statErr != nil {
		t.Errorf("a 401 that was not the API's destroyed the stored session: %v", statErr)
	}
}

func TestWhoamiUnknownFlagIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runWhoamiCapture(t, []string{"--bogus"})
	if err == nil {
		t.Fatal("whoami with an unknown flag returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("error = %v, want an unknown flag message", err)
	}
}

func TestWhoamiUnexpectedPositionalArgumentIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runWhoamiCapture(t, []string{"unexpected"})
	if err == nil {
		t.Fatal("whoami with an unexpected positional argument returned nil error")
	}
	if !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("error = %v, want an unexpected argument message", err)
	}
}

func TestWhoamiExplicitEmptyServerURLIsRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, err := runWhoamiCapture(t, []string{"--server-url="})
	if err == nil {
		t.Fatal("whoami with an explicit empty --server-url returned nil error")
	}
	if !strings.Contains(err.Error(), "--server-url must not be empty") {
		t.Fatalf("error = %v, want an empty --server-url message", err)
	}
}

func TestHumanizeUntil(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Minute, "expired"},
		{0, "expired"},
		{42 * time.Minute, "in 42m"},
		{90 * time.Minute, "in 1h 30m"},
		{25 * time.Hour, "in 25h 0m"},
	}
	for _, tc := range cases {
		if got := humanizeUntil(tc.in); got != tc.want {
			t.Errorf("humanizeUntil(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
