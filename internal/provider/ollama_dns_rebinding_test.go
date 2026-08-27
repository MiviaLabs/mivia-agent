package provider

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// installLocalhostResolver points the keyless gate's construction-time
// localhost resolution at a fixed IP for the duration of the test, ignoring
// the queried host. It restores the production resolver (net.LookupIP) via
// t.Cleanup and returns it so a test can restore early.
//
// The seam exists because a swapped net.DefaultResolver cannot control
// "localhost" on hosts-file platforms: the hosts file answers localhost
// before DNS, so the custom resolver's Dial is never consulted (verified
// against go1.26 linux: LookupIP("localhost") returns [127.0.0.1] with a
// swapped resolver and zero Dial calls). The fix under test resolves
// localhost once at construction; substituting the resolver function proves
// the same fail-closed and pin behavior the DNS-rebinding auditor targeted.
func installLocalhostResolver(t *testing.T, ip string) func(string) ([]net.IP, error) {
	t.Helper()
	orig := lookupLocalhost
	lookupLocalhost = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP(ip)}, nil
	}
	t.Cleanup(func() { lookupLocalhost = orig })
	return orig
}

// TestOllamaKeylessLocalhostFailClosedOnNonLoopbackResolution pins the fixed
// DNS-rebinding gate (plan §12 item 1): the keyless decision no longer trusts
// the literal "localhost". Construction resolves the host once and refuses
// keyless mode when it resolves anywhere other than loopback. A resolver
// answering localhost with a non-loopback address must fail closed at
// NewOllama, before any request can be dialed.
func TestOllamaKeylessLocalhostFailClosedOnNonLoopbackResolution(t *testing.T) {
	// The literal predicate still approves localhost; the fix lives at the
	// provider layer, which complements the predicate with resolution.
	if !config.IsOllamaLoopback("http://localhost:11434/v1") {
		t.Fatal("IsOllamaLoopback must approve the localhost literal; the fix is the provider-layer resolution gate")
	}

	installLocalhostResolver(t, "203.0.113.7")

	comp, err := NewOllama(Options{BaseURL: "http://localhost:11434/v1", APIKey: "sekrit"})
	if err == nil {
		t.Fatal("NewOllama must fail closed when localhost resolves to a non-loopback address")
	}
	if comp != nil {
		t.Fatalf("NewOllama returned a completer alongside error %v; must fail closed with nil", err)
	}
	if !strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error = %q, want it to mention loopback or 127.0.0.1", err)
	}
}

// TestOllamaKeylessLocalhostDialPinnedToConstructionResolution proves the
// resolve-once, pin-every-dial contract: the dial target is fixed at
// construction from the resolver state then, and every later dial rewrites
// the host to that pinned loopback IP without re-resolving. The resolver is
// restored before the dial, so a re-resolution would pick a different
// address and never reach the 127.0.0.2 listener.
func TestOllamaKeylessLocalhostDialPinnedToConstructionResolution(t *testing.T) {
	orig := installLocalhostResolver(t, "127.0.0.2")

	dial, err := newLoopbackDialContext("ollama", "http://localhost:11434/v1")
	if err != nil {
		t.Fatal(err)
	}

	// Restore the production resolver before dialing. A pinned dial must not
	// re-resolve localhost; a re-resolution would answer 127.0.0.1 (hosts
	// file) and miss the 127.0.0.2 listener below.
	lookupLocalhost = orig

	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("cannot bind a listener on 127.0.0.2 on this platform: %v", err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("pinned dial failed: %v", err)
	}
	defer conn.Close()

	serverConn, err := ln.Accept()
	if err != nil {
		t.Fatalf("listener accept failed: %v", err)
	}
	defer serverConn.Close()
	_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := serverConn.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("dialed connection did not reach the 127.0.0.2 listener: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("read %q, want pong from the 127.0.0.2 listener", buf)
	}
}

// TestNewForProviderOllamaLocalhostFailClosedUnderHostileResolver drives the
// fix end to end through the real factory path: a keyless ollama runtime
// whose localhost base URL resolves to a non-loopback address must fail
// closed at construction, not surface a missing-key error.
func TestNewForProviderOllamaLocalhostFailClosedUnderHostileResolver(t *testing.T) {
	installLocalhostResolver(t, "203.0.113.7")

	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{
		"ollama": {ProviderName: "ollama", BaseURL: "http://localhost:11434/v1", APIKeyEnv: "OLLAMA_API_KEY", APIKeySet: false},
	}}
	comp, err := NewForProvider(res, "ollama")
	if err == nil {
		t.Fatal("NewForProvider must fail closed when localhost resolves to a non-loopback address")
	}
	if comp != nil {
		t.Fatalf("NewForProvider returned a completer alongside error %v; must fail closed with nil", err)
	}
	if !strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error = %q, want it to mention loopback or 127.0.0.1", err)
	}
	if strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("error = %q: the keyless ollama gate must not surface a missing-key error", err)
	}
}

// TestOllamaKeylessLocalhostRequestIsKeylessAndUnresolved pins the request
// shape for the localhost keyless gate: no Authorization header at all (not
// even an empty Bearer), and the request targets the literal hostname.
// newLoopbackDialContext pins only the dial; the request URL keeps the
// literal localhost so the transport never re-resolves it at request time.
func TestOllamaKeylessLocalhostRequestIsKeylessAndUnresolved(t *testing.T) {
	// Hermetic: pin localhost resolution so construction does not depend on
	// the environment's resolver (no DNS server in sandboxes). Only the dial
	// is pinned; the request URL keeps the literal localhost, so the
	// assertions below are unchanged.
	installLocalhostResolver(t, "127.0.0.1")

	comp, err := NewOllama(Options{BaseURL: "http://localhost:11434/v1", APIKey: "sekrit"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	httpReq, err := client.newRequest(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := httpReq.Header["Authorization"]; present {
		t.Fatal("Authorization header must be absent for keyless ollama (an empty Bearer would be a leak)")
	}
	if httpReq.Header.Get("Authorization") != "" {
		t.Fatalf("Authorization = %q, want absent", httpReq.Header.Get("Authorization"))
	}
	if httpReq.URL.Hostname() != "localhost" {
		t.Fatalf("request hostname = %q, want the unresolved literal localhost", httpReq.URL.Hostname())
	}
}

// TestKeylessClientRetrySendsNoAuthorization pins checklist item 2 on the
// retry path: a keyless client (the shape NewOllama produces for a loopback
// daemon) must not attach an Authorization header on the first attempt nor on
// any transport retry. The retry round tripper re-sends the same request
// object, whose headers were built once by setHeaders; nothing rebuilds them
// from a key, and an empty key must never format as "Bearer " + "".
func TestKeylessClientRetrySendsNoAuthorization(t *testing.T) {
	type recorded struct {
		value   string
		present bool
	}
	var (
		mu      sync.Mutex
		auth    []recorded
		attempt int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempt++
		auth = append(auth, recorded{value: r.Header.Get("Authorization")})
		_, present := r.Header["Authorization"]
		auth[len(auth)-1].present = present
		cur := attempt
		mu.Unlock()
		if cur == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	// Keyless client, exactly as NewOllama builds it for a loopback base URL:
	// empty apiKey, stock retry transport.
	client := NewOpenAICompatWithOptions(CompatOptions{Name: "ollama", BaseURL: srv.URL, APIKey: ""})
	if _, err := client.Chat(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if attempt < 2 {
		t.Fatalf("attempts = %d, want at least one retry after 503", attempt)
	}
	for i, h := range auth {
		if h.present {
			t.Fatalf("attempt %d sent an Authorization header %q; a keyless ollama run must never send one", i+1, h.value)
		}
	}
}
