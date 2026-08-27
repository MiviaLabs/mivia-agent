package provider

// Regression coverage (F1-F10) for the Round-4 DNS-rebinding fix. TEST-ONLY:
// these tests pin the resolve-once/pin-every-dial contract of
// newLoopbackDialContext and compatBaseRoundTripper, and the CompatOptions
// DialContext threading through BOTH OpenAI-compat constructors. They are
// additive to ollama_dns_rebinding_test.go and ollama_pin_audit_test.go and
// touch no production code.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// errDialRecorded is the sentinel a recording dial func returns so a test can
// prove the exact func value is wired into a transport. Go function values are
// not comparable, so identity is proven by reflect code-pointer equality plus
// an invocation that must reach this sentinel (shared captured state).
var errDialRecorded = errors.New("recorded dial invoked")

// installLocalhostResolverIPs points the keyless gate's construction-time
// localhost resolution at a fixed set of IPs for the duration of the test,
// mirroring installLocalhostResolver (ollama_dns_rebinding_test.go) for the
// multi-IP and empty-result cases. It restores the production resolver
// (net.LookupIP) via t.Cleanup and returns the original so a test can restore
// early.
func installLocalhostResolverIPs(t *testing.T, ips []string) func(string) ([]net.IP, error) {
	t.Helper()
	orig := lookupLocalhost
	lookupLocalhost = func(string) ([]net.IP, error) {
		resolved := make([]net.IP, 0, len(ips))
		for _, s := range ips {
			resolved = append(resolved, net.ParseIP(s))
		}
		return resolved, nil
	}
	t.Cleanup(func() { lookupLocalhost = orig })
	return orig
}

// installLocalhostResolverErr points the seam at a failing resolver for the
// duration of the test, mirroring installLocalhostResolver but injecting an
// error instead of an IP. Restores the production resolver via t.Cleanup.
func installLocalhostResolverErr(t *testing.T, err error) func(string) ([]net.IP, error) {
	t.Helper()
	orig := lookupLocalhost
	lookupLocalhost = func(string) ([]net.IP, error) {
		return nil, err
	}
	t.Cleanup(func() { lookupLocalhost = orig })
	return orig
}

// F1: a dial address WITHOUT a port makes net.SplitHostPort fail inside the
// pinned dial. The unchanged-address fallback must run without panicking and
// must return a non-nil error (the dialer itself rejects a portless TCP
// address), never a connection.
func TestF1PortlessDialAddressUsesUnchangedFallback(t *testing.T) {
	dial, err := newLoopbackDialContext("ollama", "http://127.0.0.1:11434/v1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("pinned dial panicked on a portless address: %v", r)
		}
	}()
	conn, err := dial(ctx, "tcp", "127.0.0.1") // no port: net.SplitHostPort fails
	if err == nil {
		if conn != nil {
			conn.Close()
		}
		t.Fatal("dial of a portless address must return a non-nil error (unchanged-addr fallback), got a connection")
	}
	// Discriminating pin: the unchanged-address fallback (ollama_loopback.go
	// SplitHostPort-failure branch) hands the dialer '127.0.0.1' verbatim, so
	// the dialer's error carries a SINGLE colon after the host
	// ('127.0.0.1: missing port in address'). If that branch were removed, the
	// portless address would instead be re-hosted through
	// net.JoinHostPort('127.0.0.1', '') = '127.0.0.1:' - a syntactically
	// valid address (empty port means port 0) that parses fine, so the dial
	// would fail with a port-0 'connection refused' carrying NO 'missing port'
	// text. The single assertion below is therefore the real discriminator:
	// it cannot pass vacuously when the fallback branch is deleted.
	if !strings.Contains(err.Error(), "127.0.0.1: missing port in address") {
		t.Fatalf("error = %q, want it to contain %q (unchanged-address fallback must dial '127.0.0.1' unchanged)", err, "127.0.0.1: missing port in address")
	}
}

// F2a: localhost resolves to [127.0.0.2, 127.0.0.3]; the multi-IP loop must
// try the NEXT pinned IP when the first one fails to connect. The listener is
// bound to 127.0.0.3 only, so a successful accept + pong exchange proves the
// dial reached the second pinned address after 127.0.0.2 refused.
func TestF2AMultiIPLoopTriesNextPinnedIP(t *testing.T) {
	installLocalhostResolverIPs(t, []string{"127.0.0.2", "127.0.0.3"})

	dial, err := newLoopbackDialContext("ollama", "http://localhost:11434/v1")
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.3:0")
	if err != nil {
		t.Skipf("cannot bind a listener on 127.0.0.3 on this platform: %v", err)
	}
	defer ln.Close()
	if tl, ok := ln.(*net.TCPListener); ok {
		_ = tl.SetDeadline(time.Now().Add(10 * time.Second))
	}
	port := portOf(t, ln.Addr().String())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("multi-IP pinned dial failed: %v", err)
	}
	defer conn.Close()

	serverConn, err := ln.Accept()
	if err != nil {
		t.Fatalf("listener on 127.0.0.3 never received the connection: %v", err)
	}
	defer serverConn.Close()
	_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := serverConn.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("dialed connection did not reach the 127.0.0.3 listener: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("read %q, want pong from the 127.0.0.3 listener", buf)
	}
}

// F2b: localhost resolves to a single pinned IP [127.0.0.2]; when that
// address refuses (listener bound then closed), the dial must surface the
// first (only) attempt's error, containing "connection refused".
func TestF2BSinglePinnedIPRefusalSurfacesFirstErr(t *testing.T) {
	installLocalhostResolverIPs(t, []string{"127.0.0.2"})

	dial, err := newLoopbackDialContext("ollama", "http://localhost:11434/v1")
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("cannot bind a listener on 127.0.0.2 on this platform: %v", err)
	}
	port := portOf(t, ln.Addr().String())
	// Close the listener so the pinned dial's only attempt is refused.
	ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", net.JoinHostPort("localhost", port))
	if err == nil {
		if conn != nil {
			conn.Close()
		}
		t.Fatal("dial to a closed pinned port must fail")
	}
	if !wantDialRefused(err) {
		t.Fatalf("error = %q, want it to contain \"connection refused\"", err)
	}
}

// F3: a literal non-loopback address must be refused at construction.
func TestF3NonLoopbackLiteralAddressRejected(t *testing.T) {
	_, err := newLoopbackDialContext("ollama", "http://203.0.113.7:11434/v1")
	if err == nil {
		t.Fatal("newLoopbackDialContext must reject a non-loopback literal address")
	}
	if !strings.Contains(err.Error(), "not a loopback address") {
		t.Fatalf("error = %q, want it to mention \"not a loopback address\"", err)
	}
}

// F4: a base URL with no hostname must be refused at construction.
func TestF4HostlessBaseURLRejected(t *testing.T) {
	_, err := newLoopbackDialContext("ollama", "http://:11434/v1")
	if err == nil {
		t.Fatal("newLoopbackDialContext must reject a hostless base URL")
	}
	if !strings.Contains(err.Error(), "no hostname") {
		t.Fatalf("error = %q, want it to mention \"no hostname\"", err)
	}
}

// F5: an unparseable base URL must be refused at construction.
func TestF5UnparseableBaseURLRejected(t *testing.T) {
	_, err := newLoopbackDialContext("ollama", "://bad")
	if err == nil {
		t.Fatal("newLoopbackDialContext must reject an unparseable base URL")
	}
	if !strings.Contains(err.Error(), "parse base_url") {
		t.Fatalf("error = %q, want it to mention \"parse base_url\"", err)
	}
}

// F6: a failing resolver must fail the keyless gate closed at construction.
func TestF6ResolverErrorFailsClosed(t *testing.T) {
	installLocalhostResolverErr(t, errors.New("hostile resolver"))
	_, err := newLoopbackDialContext("ollama", "http://localhost:11434/v1")
	if err == nil {
		t.Fatal("newLoopbackDialContext must fail closed when the resolver errors")
	}
	if !strings.Contains(err.Error(), "cannot resolve localhost") {
		t.Fatalf("error = %q, want it to mention \"cannot resolve localhost\"", err)
	}
}

// F7: a resolver returning an empty slice must fail the keyless gate closed at
// construction (nothing to pin).
func TestF7EmptyResolverResultFailsClosed(t *testing.T) {
	installLocalhostResolverIPs(t, nil)
	_, err := newLoopbackDialContext("ollama", "http://localhost:11434/v1")
	if err == nil {
		t.Fatal("newLoopbackDialContext must fail closed when the resolver returns no addresses")
	}
	if !strings.Contains(err.Error(), "no loopback addresses") {
		t.Fatalf("error = %q, want it to mention \"no loopback addresses\"", err)
	}
}

// F8: a non-loopback hostname (not localhost, not a literal loopback IP) must
// be refused at construction.
func TestF8NonLoopbackHostRejected(t *testing.T) {
	_, err := newLoopbackDialContext("ollama", "http://ollama.example.com:11434/v1")
	if err == nil {
		t.Fatal("newLoopbackDialContext must reject a non-loopback hostname")
	}
	if !strings.Contains(err.Error(), "not a loopback host") {
		t.Fatalf("error = %q, want it to mention \"not a loopback host\"", err)
	}
}

// F9: CompatOptions.DialContext must thread through BOTH OpenAI-compat
// constructors into the retry round tripper's inner transport clone. Proven
// directly: the inner transport is the *http.Transport clone (not
// DefaultTransport), its DialContext is the provided recording dial func
// (reflect code-pointer identity), and invoking it reaches the recorder's
// shared captured state (sentinel error + recorded address).
func TestF9DialContextThreadsIntoRetryRoundTripper(t *testing.T) {
	var (
		mu       sync.Mutex
		recorded []string
	)
	rec := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		recorded = append(recorded, addr)
		mu.Unlock()
		return nil, errDialRecorded
	}

	const wantAddr = "127.0.0.1:11434"
	assertWired := func(t *testing.T, c *OpenAICompat) {
		t.Helper()
		inner, ok := innerTransport(c).(*http.Transport)
		if !ok {
			t.Fatalf("inner transport = %T, want *http.Transport clone", innerTransport(c))
		}
		if inner == http.DefaultTransport {
			t.Fatal("inner transport is http.DefaultTransport; DialContext was not threaded")
		}
		if inner.DialContext == nil {
			t.Fatal("inner transport DialContext is nil despite CompatOptions.DialContext")
		}
		if reflect.ValueOf(inner.DialContext).Pointer() != reflect.ValueOf(rec).Pointer() {
			t.Fatal("inner transport DialContext is not the provided dial func")
		}
		// Invocation proof: the exact same closure state must be reached.
		mu.Lock()
		recorded = recorded[:0]
		mu.Unlock()
		if _, err := inner.DialContext(context.Background(), "tcp", wantAddr); err != errDialRecorded {
			t.Fatalf("inner transport dial returned %v, want the recorded sentinel %v", err, errDialRecorded)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(recorded) != 1 || recorded[0] != wantAddr {
			t.Fatalf("recorded dials = %v, want exactly [%q]", recorded, wantAddr)
		}
	}

	t.Run("NewOpenAICompatWithOptionsAndRetry", func(t *testing.T) {
		c := NewOpenAICompatWithOptionsAndRetry(CompatOptions{
			Name:        "ollama",
			BaseURL:     "http://127.0.0.1:11434/v1",
			DialContext: rec,
		}, &retryOptions{})
		assertWired(t, c)
	})

	t.Run("NewOpenAICompatWithOptions", func(t *testing.T) {
		c := NewOpenAICompatWithOptions(CompatOptions{
			Name:        "ollama",
			BaseURL:     "http://127.0.0.1:11434/v1",
			DialContext: rec,
		})
		assertWired(t, c)
	})
}

// F10: compatBaseRoundTripper(nil) must return http.DefaultTransport by
// pointer identity (no clone, no wrapper); compatBaseRoundTripper(rec) must
// return a *http.Transport clone - distinct from DefaultTransport - whose
// DialContext is rec (spot-checked identity) and whose other fields match the
// DefaultTransport defaults.
func TestF10CompatBaseRoundTripperNilAndClone(t *testing.T) {
	if got := compatBaseRoundTripper(nil); got != http.DefaultTransport {
		t.Fatalf("compatBaseRoundTripper(nil) = %T, want http.DefaultTransport identity", got)
	}

	var (
		mu       sync.Mutex
		recorded []string
	)
	rec := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		recorded = append(recorded, addr)
		mu.Unlock()
		return nil, errDialRecorded
	}

	got := compatBaseRoundTripper(rec)
	clone, ok := got.(*http.Transport)
	if !ok {
		t.Fatalf("compatBaseRoundTripper(rec) = %T, want *http.Transport", got)
	}
	dflt := http.DefaultTransport.(*http.Transport)
	if clone == dflt {
		t.Fatal("compatBaseRoundTripper(rec) must return a clone, not http.DefaultTransport itself")
	}
	if clone.DialContext == nil {
		t.Fatal("clone DialContext is nil")
	}
	if reflect.ValueOf(clone.DialContext).Pointer() != reflect.ValueOf(rec).Pointer() {
		t.Fatal("clone DialContext is not the provided dial func")
	}
	// Spot-check that the clone keeps the DefaultTransport field defaults.
	if clone.MaxIdleConns != dflt.MaxIdleConns {
		t.Fatalf("clone.MaxIdleConns = %d, want default %d", clone.MaxIdleConns, dflt.MaxIdleConns)
	}
	if clone.IdleConnTimeout != dflt.IdleConnTimeout {
		t.Fatalf("clone.IdleConnTimeout = %v, want default %v", clone.IdleConnTimeout, dflt.IdleConnTimeout)
	}
	// Invocation proof: the recorded sentinel must come back through the clone.
	mu.Lock()
	recorded = recorded[:0]
	mu.Unlock()
	const wantAddr = "127.0.0.1:11434"
	if _, err := clone.DialContext(context.Background(), "tcp", wantAddr); err != errDialRecorded {
		t.Fatalf("clone dial returned %v, want the recorded sentinel %v", err, errDialRecorded)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 1 || recorded[0] != wantAddr {
		t.Fatalf("recorded dials = %v, want exactly [%q]", recorded, wantAddr)
	}
}
