package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// seedDefaultSession writes a valid, non-expired token to the standard
// UserAuthPath under a temp HOME, so miviaauth.HasDefaultSession() reports
// true without touching a real logged-in session.
func seedDefaultSession(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}
	path := config.UserAuthPath()
	tok := miviaauth.Token{
		Bearer:       "bearer-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := miviaauth.Save(path, tok); err != nil {
		t.Fatal(err)
	}
	if !miviaauth.HasDefaultSession() {
		t.Fatal("precondition: seeded token was not recognized as a default session")
	}
}

// TestDefaultTokenProvider_DefaultServiceErrorIsNil pins
// DefaultTokenProvider's own DefaultService-error branch: a session exists
// (HasDefaultSession is true) but DefaultService itself cannot build a
// client, forced here by a versioned MIVIA_API_BASE_URL (NewClient's own
// refusal, not a missing-HOME condition, which would also fail
// HasDefaultSession and so could not isolate this branch).
func TestDefaultTokenProvider_DefaultServiceErrorIsNil(t *testing.T) {
	seedDefaultSession(t)
	t.Setenv("MIVIA_API_BASE_URL", "https://api.example.com/v1")
	if got := DefaultTokenProvider(); got != nil {
		t.Fatal("DefaultTokenProvider did not return nil when DefaultService itself fails")
	}
}

// TestDefaultAuthorUserIDProvider_DefaultServiceErrorIsNil mirrors the
// above for DefaultAuthorUserIDProvider's identical guard.
func TestDefaultAuthorUserIDProvider_DefaultServiceErrorIsNil(t *testing.T) {
	seedDefaultSession(t)
	t.Setenv("MIVIA_API_BASE_URL", "https://api.example.com/v1")
	if got := DefaultAuthorUserIDProvider(); got != nil {
		t.Fatal("DefaultAuthorUserIDProvider did not return nil when DefaultService itself fails")
	}
}

// TestDefaultAuthorUserIDProvider_WhoamiErrorSurfaces pins the closure's
// own Whoami-error propagation: a session exists and DefaultService
// builds fine, but the network call inside the returned closure fails.
func TestDefaultAuthorUserIDProvider_WhoamiErrorSurfaces(t *testing.T) {
	seedDefaultSession(t)
	// An unreachable loopback port keeps the Whoami call fast and local
	// while still guaranteeing a network error.
	t.Setenv("MIVIA_API_BASE_URL", "http://127.0.0.1:1")
	provide := DefaultAuthorUserIDProvider()
	if provide == nil {
		t.Fatal("precondition: DefaultAuthorUserIDProvider returned nil, want a usable closure")
	}
	if _, err := provide(t.Context()); err == nil {
		t.Fatal("the closure hid a Whoami failure")
	}
}

// TestDefaultAuthorUserIDProvider_WhoamiSuccessReturnsIdentityID pins the
// closure's own success path, distinct from the error-propagation test
// above: a real (local, test-only) API answering /v1/auth/me returns the
// identity id.
func TestDefaultAuthorUserIDProvider_WhoamiSuccessReturnsIdentityID(t *testing.T) {
	seedDefaultSession(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("MIVIA_API_BASE_URL", server.URL)

	provide := DefaultAuthorUserIDProvider()
	if provide == nil {
		t.Fatal("precondition: DefaultAuthorUserIDProvider returned nil, want a usable closure")
	}
	id, err := provide(context.Background())
	if err != nil {
		t.Fatalf("provide() error = %v", err)
	}
	if id != "user-123" {
		t.Fatalf("provide() = %q, want the identity id from /v1/auth/me", id)
	}
}
