package chatsync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

type stubEnsurer struct {
	token string
	err   error
	calls atomic.Int64
}

func (s *stubEnsurer) Ensure(context.Context) (string, error) {
	s.calls.Add(1)
	return s.token, s.err
}

// TestNewTokenProviderClassifiesAuthErrors pins the settled 401 policy's
// error split. Getting this backwards is not cosmetic: a transient
// ErrRefreshBusy treated as fatal silently stops syncing a healthy session,
// and a fatal ErrReauthRequired treated as transient turns into a retry loop
// against a session that is gone.
func TestNewTokenProviderClassifiesAuthErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantStop bool
	}{
		{"refresh busy is transient", miviaauth.ErrRefreshBusy, false},
		{"reauth required stops sync", miviaauth.ErrReauthRequired, true},
		{"session lost stops sync", miviaauth.ErrSessionLost, true},
		{"unknown error is transient", errors.New("dial tcp: timeout"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewTokenProvider(&stubEnsurer{err: tc.err})
			_, err := provider(context.Background(), false)
			if err == nil {
				t.Fatal("provider error = nil, want an error")
			}
			if got := errors.Is(err, ErrAuthStop); got != tc.wantStop {
				t.Errorf("errors.Is(err, ErrAuthStop) = %v, want %v (err = %v)", got, tc.wantStop, err)
			}
			if !errors.Is(err, tc.err) {
				t.Errorf("err = %v; the cause must stay inspectable", err)
			}
		})
	}
}

// TestNewTokenProviderNilEnsurer keeps the fail-closed chain intact: a nil
// service produces a nil provider, which NewClient then refuses.
func TestNewTokenProviderNilEnsurer(t *testing.T) {
	if p := NewTokenProvider(nil); p != nil {
		t.Fatal("NewTokenProvider(nil) != nil; a nil service must not become a usable provider")
	}
}

// TestNewTokenProviderIgnoresForceRefresh pins that the retry path does not
// spend a second refresh of its own. Ensure owns the refresh decision under
// the cross-process lock, and a second refresher revokes the session.
func TestNewTokenProviderIgnoresForceRefresh(t *testing.T) {
	stub := &stubEnsurer{token: "tok"}
	provider := NewTokenProvider(stub)
	for _, force := range []bool{false, true} {
		got, err := provider(context.Background(), force)
		if err != nil || got != "tok" {
			t.Fatalf("provider(force=%v) = %q, %v", force, got, err)
		}
	}
	if n := stub.calls.Load(); n != 2 {
		t.Errorf("Ensure calls = %d, want 2 (one per request, never a private refresh)", n)
	}
}

// TestClientLatchesFatalAuthFailure pins the "stop sync" half of the policy:
// after a fatal token error the client stops issuing requests entirely,
// rather than re-entering the refresh path on every flush tick.
func TestClientLatchesFatalAuthFailure(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	provider := NewTokenProvider(&stubEnsurer{err: miviaauth.ErrReauthRequired})
	client, err := NewClient(provider, ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if _, err := client.GetSession(context.Background(), "sess-1"); !errors.Is(err, ErrAuthStop) {
		t.Fatalf("first GetSession error = %v, want ErrAuthStop", err)
	}
	if !client.AuthLost() {
		t.Fatal("AuthLost() = false after a fatal token error")
	}
	if _, err := client.GetSession(context.Background(), "sess-1"); !errors.Is(err, ErrAuthStop) {
		t.Fatalf("second GetSession error = %v, want ErrAuthStop", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("server saw %d request(s); a lost session must stop talking to the API", n)
	}
}

// TestClientRetriesOnceOn401 pins "Ensure once and retry", and that the retry
// is not repeated: a second 401 is a rejected session, not a stale token.
func TestClientRetriesOnceOn401(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client, err := NewClient(testTokenProvider, ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.GetSession(context.Background(), "sess-1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("GetSession error = %v, want ErrUnauthorized", err)
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 (one attempt plus exactly one retry)", n)
	}
}

// TestClientRefusesEmptyToken keeps the header non-optional: a provider that
// returns "" with no error must not produce an anonymous request.
func TestClientRefusesEmptyToken(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewClient(func(context.Context, bool) (string, error) { return "", nil }, ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.GetSession(context.Background(), "sess-1"); !errors.Is(err, ErrEmptyToken) {
		t.Fatalf("GetSession error = %v, want ErrEmptyToken", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("server saw %d request(s), want 0", n)
	}
}
