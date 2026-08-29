package provider

// A transport stage timeout must never be read as the caller's own deadline.
// These tests pin both halves of that claim: the stdlib error shapes this
// package depends on (a real ctx deadline carries the sentinel by identity, a
// header timeout only claims equality with it), and the classification every
// caller reads.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The discriminator over synthetic errors: only an error that MERELY reports
// itself equal to context.DeadlineExceeded is a stage timeout.
func TestIsTransportStageTimeout(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bare context deadline", context.DeadlineExceeded, false},
		{"wrapped context deadline", fmt.Errorf("request budget: %w", context.DeadlineExceeded), false},
		{"double wrapped context deadline", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", context.DeadlineExceeded)), false},
		{"joined context deadline", errors.Join(errors.New("other"), context.DeadlineExceeded), false},
		{"context canceled", context.Canceled, false},
		{"ordinary error", errors.New("boom"), false},
		{"claims equality only", claimsDeadlineEquality{}, true},
		{"wrapped claim", fmt.Errorf("provider: %w", claimsDeadlineEquality{}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransportStageTimeout(tc.err); got != tc.want {
				t.Fatalf("IsTransportStageTimeout(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// claimsDeadlineEquality mimics net/http's unexported timeoutError: it reports
// equality with context.DeadlineExceeded without any context having expired.
type claimsDeadlineEquality struct{}

func (claimsDeadlineEquality) Error() string { return "net/http: timeout awaiting response headers" }
func (claimsDeadlineEquality) Is(err error) bool {
	return err == context.DeadlineExceeded
}
func (claimsDeadlineEquality) Timeout() bool   { return true }
func (claimsDeadlineEquality) Temporary() bool { return true }

// Stdlib pin. The discriminator rests on a property of net/http's error
// shapes; if a future Go release changes which timer carries the sentinel by
// identity, this test fails here rather than silently re-arming the bug.
func TestStdlibTimerErrorIdentities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()

	t.Run("response header timeout is a stage timeout", func(t *testing.T) {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.ResponseHeaderTimeout = 50 * time.Millisecond
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		_, err := (&http.Client{Transport: base}).Do(req)
		if err == nil {
			t.Fatal("expected the header bound to fire")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stdlib no longer reports header timeout as a deadline: %v", err)
		}
		if !IsTransportStageTimeout(err) {
			t.Fatalf("header timeout must classify as a transport stage timeout, got err=%v", err)
		}
	})

	t.Run("request context deadline is not a stage timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		_, err := (&http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}).Do(req)
		if err == nil {
			t.Fatal("expected the request deadline to fire")
		}
		if IsTransportStageTimeout(err) {
			t.Fatalf("a real caller deadline must not classify as a transport stage timeout: %v", err)
		}
	})
}

// A stage timeout says the call never delivered an answer, so it is transient:
// a fresh call can clear it. A caller deadline stays non-transient.
func TestIsTransientTreatsStageTimeoutAsTransient(t *testing.T) {
	if !IsTransient(claimsDeadlineEquality{}) {
		t.Fatal("a transport stage timeout must be transient")
	}
	if IsTransient(context.DeadlineExceeded) {
		t.Fatal("a real caller deadline must not be transient")
	}
	if IsTransient(fmt.Errorf("step budget: %w", context.DeadlineExceeded)) {
		t.Fatal("a wrapped caller deadline must not be transient")
	}
}
