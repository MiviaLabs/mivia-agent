package uiadapter

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// fakeAuthClient is a hand-written test double for the sessionClient
// interface miviaauth.Service depends on. It has no import of miviaauth's
// unexported interface type: Go's structural typing lets it satisfy
// miviaauth.NewService's parameter just by having the matching methods,
// the same trick internal/miviaauth/service_test.go's own fakeSessionClient
// uses one package over.
type fakeAuthClient struct {
	loginToken miviaauth.Token
	loginErr   error
}

func (f *fakeAuthClient) Login(_ context.Context, _ string, _ []byte) (miviaauth.Token, error) {
	return f.loginToken, f.loginErr
}
func (f *fakeAuthClient) Refresh(context.Context, string) (miviaauth.Token, error) {
	return miviaauth.Token{}, errors.New("not implemented")
}
func (f *fakeAuthClient) Revoke(context.Context, string, string) error {
	return errors.New("not implemented")
}
func (f *fakeAuthClient) Me(context.Context, string) (miviaauth.Identity, error) {
	return miviaauth.Identity{}, errors.New("not implemented")
}

// newLoginTestRunner builds a CommandRunner whose loginService seam
// resolves to a Service wired over fake, so CompleteLogin tests never
// touch the network or the real ~/.mivia/auth.json.
func newLoginTestRunner(t *testing.T, fake *fakeAuthClient) *CommandRunner {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	r := NewCommandRunner(nil, nil, nil)
	r.loginService = func() (*miviaauth.Service, error) {
		return miviaauth.NewService(fake, path), nil
	}
	return r
}

func TestHandleLoginNoArgsOpensDialogWithoutPrefill(t *testing.T) {
	r := NewCommandRunner(nil, nil, nil)
	out := r.Run(context.Background(), "login", "")
	if !out.LoginPrompt {
		t.Fatal("expected LoginPrompt=true")
	}
	if out.LoginEmail != "" {
		t.Errorf("got LoginEmail %q, want empty", out.LoginEmail)
	}
}

func TestHandleLoginOneArgPrefillsEmail(t *testing.T) {
	r := NewCommandRunner(nil, nil, nil)
	out := r.Run(context.Background(), "login", "user@example.com")
	if !out.LoginPrompt {
		t.Fatal("expected LoginPrompt=true")
	}
	if out.LoginEmail != "user@example.com" {
		t.Errorf("got LoginEmail %q, want user@example.com", out.LoginEmail)
	}
}

func TestHandleLoginTooManyArgsIsAnError(t *testing.T) {
	r := NewCommandRunner(nil, nil, nil)
	out := r.Run(context.Background(), "login", "user@example.com extra")
	if out.LoginPrompt {
		t.Error("expected LoginPrompt=false on a usage error")
	}
	if !strings.Contains(out.Err, "usage: /login") {
		t.Errorf("got Err %q, want a usage message", out.Err)
	}
	if strings.Contains(out.Err, "extra") {
		t.Errorf("usage error should not echo the offending arguments verbatim, got %q", out.Err)
	}
}

func TestCompleteLoginSuccessReportsNotice(t *testing.T) {
	fake := &fakeAuthClient{loginToken: miviaauth.Token{Bearer: "b", RefreshToken: "rt"}}
	r := newLoginTestRunner(t, fake)

	pw := []byte("hunter2")
	out := r.CompleteLogin(context.Background(), "user@example.com", pw)

	if out.Err != "" {
		t.Fatalf("got Err %q, want success", out.Err)
	}
	if out.Notice != "Logged in as user@example.com." {
		t.Errorf("got Notice %q, want the login confirmation", out.Notice)
	}
}

func TestCompleteLoginZeroesThePasswordBuffer(t *testing.T) {
	fake := &fakeAuthClient{loginToken: miviaauth.Token{Bearer: "b", RefreshToken: "rt"}}
	r := newLoginTestRunner(t, fake)

	pw := []byte("hunter2")
	r.CompleteLogin(context.Background(), "user@example.com", pw)

	if !bytes.Equal(pw, make([]byte, len(pw))) {
		t.Errorf("password buffer not zeroed after CompleteLogin, got %v", pw)
	}
}

func TestCompleteLoginEmptyEmailIsRejected(t *testing.T) {
	r := newLoginTestRunner(t, &fakeAuthClient{})
	out := r.CompleteLogin(context.Background(), "", []byte("pw"))
	if out.Err != "enter a valid email address" {
		t.Errorf("got Err %q, want the email validation message", out.Err)
	}
}

func TestCompleteLoginEmailWithoutAtIsRejected(t *testing.T) {
	r := newLoginTestRunner(t, &fakeAuthClient{})
	out := r.CompleteLogin(context.Background(), "not-an-email", []byte("pw"))
	if out.Err != "enter a valid email address" {
		t.Errorf("got Err %q, want the email validation message", out.Err)
	}
}

func TestCompleteLoginEmptyPasswordIsRejected(t *testing.T) {
	r := newLoginTestRunner(t, &fakeAuthClient{})
	out := r.CompleteLogin(context.Background(), "user@example.com", nil)
	if out.Err != "enter your password" {
		t.Errorf("got Err %q, want the password validation message", out.Err)
	}
}

func statusErr(code int, detail string) error {
	return &miviaauth.StatusError{StatusCode: code, FromAPI: true, Detail: detail}
}

func TestCompleteLoginMapsServerErrors(t *testing.T) {
	cases := []struct {
		name     string
		loginErr error
		want     string
	}{
		{
			name:     "401 unauthorized",
			loginErr: statusErr(http.StatusUnauthorized, "Invalid or expired session"),
			want:     "the email or password was not accepted. Check the password, or sign up at https://mivia.app if you do not have an account yet",
		},
		{
			name:     "400 with detail",
			loginErr: statusErr(http.StatusBadRequest, "email must be a valid address"),
			want:     "the server rejected the request: email must be a valid address",
		},
		{
			name:     "429 rate limited",
			loginErr: statusErr(http.StatusTooManyRequests, ""),
			want:     "rate limited, wait a few minutes and try again",
		},
		{
			name:     "wrapped network error",
			loginErr: errors.New("dial tcp: connection refused"),
			want:     "login failed:",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeAuthClient{loginErr: c.loginErr}
			r := newLoginTestRunner(t, fake)
			out := r.CompleteLogin(context.Background(), "user@example.com", []byte("hunter2"))
			if out.Notice != "" {
				t.Errorf("got Notice %q, want empty on failure", out.Notice)
			}
			if !strings.Contains(out.Err, c.want) {
				t.Errorf("got Err %q, want it to contain %q", out.Err, c.want)
			}
			if strings.Contains(out.Err, "hunter2") {
				t.Errorf("error text leaked the password: %q", out.Err)
			}
		})
	}
}

// TestCompleteLoginNoOutcomeTextContainsThePassword is a blanket guard
// across every branch: whatever CompleteLogin returns, the password never
// appears in it, success or failure.
func TestCompleteLoginNoOutcomeTextContainsThePassword(t *testing.T) {
	const password = "correct-horse-battery-staple"
	cases := []struct {
		name string
		fake *fakeAuthClient
	}{
		{name: "success", fake: &fakeAuthClient{loginToken: miviaauth.Token{Bearer: "b", RefreshToken: "rt"}}},
		{name: "401", fake: &fakeAuthClient{loginErr: statusErr(http.StatusUnauthorized, "")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newLoginTestRunner(t, c.fake)
			pw := []byte(password)
			out := r.CompleteLogin(context.Background(), "user@example.com", pw)
			if strings.Contains(out.Notice, password) || strings.Contains(out.Err, password) {
				t.Fatalf("outcome leaked the password: %+v", out)
			}
		})
	}
}
