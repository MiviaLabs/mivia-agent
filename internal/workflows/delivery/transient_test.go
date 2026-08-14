package delivery

// IsTransportFault classifies git/gh transport faults: a delivery attempt
// that died on the network is not a condition in the change, so the settle
// paths must not dispatch a repair agent or write a failure record for it -
// the run stays delivery_pending and a later deliver succeeds.

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestIsTransportFault(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// git transport texts (fatal: ... prefixes arrive from CombinedOutput).
		{"dns", errors.New("git fetch: fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com"), true},
		{"tcp timeout", errors.New("git fetch: fatal: unable to access 'https://github.com/x/y.git/': Failed to connect to github.com port 443: Connection timed out"), true},
		{"refused", errors.New("git push: fatal: unable to access 'https://github.com/x/y.git/': Failed to connect to 127.0.0.1 port 22: Connection refused"), true},
		{"reset", errors.New("git fetch: fatal: read tcp 10.0.0.2:4444->140.82.121.4:443: read: connection reset by peer"), true},
		{"unreachable", errors.New("git push: fatal: unable to access 'https://github.com/x/y.git/': Failed to connect to github.com port 443: Network is unreachable"), true},
		{"gh connect", errors.New("gh: failed to connect to github.com"), true},
		// Wrapped net/syscall errors (rare, but the type checks are cheap).
		{"net timeout", &netOpError{timeout: true}, true},
		{"econnreset", os.NewSyscallError("read", syscall.ECONNRESET), true},
		{"econnrefused", os.NewSyscallError("dial", syscall.ECONNREFUSED), true},
		// Not transport faults.
		{"host rejection", errors.New("host rejected the change: branch protection requires a linked issue"), false},
		{"refusal text", errors.New("delivery base was rewritten since admission"), false},
		{"hook rejection", errors.New("commit hook refused the change: subject line too long"), false},
		{"deadline", context.DeadlineExceeded, false},
		{"cancelled", context.Canceled, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransportFault(tc.err); got != tc.want {
				t.Fatalf("IsTransportFault(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// netOpError is a minimal net.Error whose only behavior is Timeout().
type netOpError struct {
	timeout bool
}

func (e *netOpError) Error() string   { return "net op error" }
func (e *netOpError) Timeout() bool   { return e.timeout }
func (e *netOpError) Temporary() bool { return e.timeout }

var _ net.Error = (*netOpError)(nil)

// TestIsTransportFaultIgnoresUnrelatedTimeoutText pins the narrowness rule:
// the word "timed out" alone is not a match - a slow hook or a lint that
// "timed out" is a condition in the environment, not a network death. Only
// the full connection phrases match.
func TestIsTransportFaultIgnoresUnrelatedTimeoutText(t *testing.T) {
	if IsTransportFault(errors.New("evidence command timed out after 30s")) {
		t.Fatal("plain 'timed out' matched, want only connection-phrased transport faults")
	}
	if IsTransportFault(errors.New("git merge-file: operation timed out")) {
		t.Fatal("bare 'timed out' after a program name matched, want only connection-phrased transport faults")
	}
	_ = time.Second // keep time imported for future table entries
}
