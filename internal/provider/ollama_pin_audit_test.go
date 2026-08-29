package provider

// Hostile re-audit (commit 4b745468) of the keyless ollama DNS-rebinding
// fix. These tests are TEST-ONLY audit pins; production code is not touched.
// They prove the resolve-once/pin-every-dial contract holds across every
// construction path, both constructors, redirects, https dials, multi-address
// localhost resolution, and every request shape, and that the fail-closed
// errors surface without leaking the key.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// innerTransport unwraps the retry wrapper to the base transport a client was
// built on.
func innerTransport(c *OpenAICompat) http.RoundTripper {
	rt, ok := c.client.Transport.(*retryRoundTripper)
	if !ok {
		return nil
	}
	return rt.inner
}

// anyTransport returns one of the client's phase transports. They are built
// from one template and differ only in their header bound, so a test whose
// subject is the dial wiring needs just one. Tests whose subject IS the
// per-pool coverage of the pin use httpTransportsOf and assert over all of
// them.
func anyTransport(c *OpenAICompat) *http.Transport {
	if transports := httpTransportsOf(c); len(transports) > 0 {
		return transports[0]
	}
	return nil
}

// modalOf returns the client's per-phase transport pair, failing the test when
// the base round tripper is not the expected shape.
func modalOf(t *testing.T, c *OpenAICompat) *modalHeaderTransport {
	t.Helper()
	modal, ok := innerTransport(c).(*modalHeaderTransport)
	if !ok {
		t.Fatalf("inner transport = %T, want *modalHeaderTransport", innerTransport(c))
	}
	return modal
}

// httpTransportsOf returns every *http.Transport a client's base round tripper
// owns. A client carries one transport per header phase (see header_bound.go),
// and the loopback pin is a security gate that must hold on EVERY one of them
// - a pin applied to only the streaming pool would still let a non-stream
// request dial whatever a resolver answered. Callers assert over the whole
// slice, and fail when it is empty.
func httpTransportsOf(c *OpenAICompat) []*http.Transport {
	switch base := innerTransport(c).(type) {
	case *modalHeaderTransport:
		return []*http.Transport{base.streamed, base.generation}
	case *http.Transport:
		return []*http.Transport{base}
	default:
		return nil
	}
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// writeChatJSON writes a minimal valid chat completion response.
func writeChatJSON(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": content},
		}},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// writeSSE writes SSE frames whose payloads are the pre-marshalled chunk maps.
func writeSSE(w http.ResponseWriter, chunks ...map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		b, err := json.Marshal(c)
		if err != nil {
			continue
		}
		_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func sseDelta(content string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": content}}}}
}

func sseDone(content string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": content}, "finish_reason": "stop"}}}
}

// TestAuditEveryKeylessConstructionPathInstallsPinnedTransport pins that
// EVERY production construction path for a keyless ollama client installs the
// pinned dial: NewOllama for 127.0.0.1 / ::1 / localhost, and NewForProvider
// for a loopback runtime. None may fall back to http.DefaultTransport, and
// the clone must keep DialTLSContext nil so https dials also use the pin.
func TestAuditEveryKeylessConstructionPathInstallsPinnedTransport(t *testing.T) {
	installLocalhostResolver(t, "127.0.0.2") // localhost variants
	cases := []struct {
		name    string
		baseURL string
	}{
		{"ipv4", "http://127.0.0.1:11434/v1"},
		{"ipv6", "http://[::1]:11434/v1"},
		{"localhost", "http://localhost:11434/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comp, err := NewOllama(Options{BaseURL: tc.baseURL, APIKey: "sekrit"})
			if err != nil {
				t.Fatalf("NewOllama: %v", err)
			}
			client := comp.(*OpenAICompat)
			if client.apiKey != "" {
				t.Fatalf("apiKey = %q, want stripped", client.apiKey)
			}
			transports := httpTransportsOf(client)
			if len(transports) == 0 {
				t.Fatalf("inner transport = %T, want pinned *http.Transport clones", innerTransport(client))
			}
			for _, tr := range transports {
				if tr == http.DefaultTransport {
					t.Fatal("inner transport is http.DefaultTransport; keyless ollama dial is NOT pinned")
				}
				if tr.DialContext == nil {
					t.Fatal("pinned clone has nil DialContext")
				}
				if tr.DialTLSContext != nil {
					t.Fatal("pinned clone has DialTLSContext set; https dials could bypass DialContext")
				}
			}
		})
	}
	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{
		"ollama": {ProviderName: "ollama", BaseURL: "http://localhost:11434/v1", APIKeyEnv: "OLLAMA_API_KEY", APIKeySet: false},
	}}
	comp, err := NewForProvider(res, "ollama")
	if err != nil {
		t.Fatalf("NewForProvider: %v", err)
	}
	client := comp.(*OpenAICompat)
	transports := httpTransportsOf(client)
	if len(transports) == 0 {
		t.Fatalf("NewForProvider keyless ollama base transport = %T, want pinned clones", innerTransport(client))
	}
	for _, tr := range transports {
		if tr == http.DefaultTransport || tr.DialContext == nil {
			t.Fatalf("NewForProvider keyless ollama transport is DefaultTransport=%v DialContext=%v; want pinned clone", tr == http.DefaultTransport, tr.DialContext != nil)
		}
	}
}

// TestAuditNonOllamaProvidersKeepDefaultTransport pins the common (non-loopback)
// cloud case: a cloud base_url never gets a PINNED dial. Its client gets a
// fresh clone of the default transport - never the global itself - whose
// dialer stays the DEFAULT one and whose only transport change is the
// response-header bound. NewForProvider also sets DialContext for a
// non-ollama provider on a VERIFIED LOOPBACK base_url now (see
// TestNewForProviderPinsLoopbackDialForNonOllamaProvider) - every case below
// uses an https/cloud BaseURL specifically so none of them exercise that path.
func TestAuditNonOllamaProvidersKeepDefaultTransport(t *testing.T) {
	def := http.DefaultTransport.(*http.Transport)
	cases := []struct {
		name         string
		providerName string
		res          *config.Resolved
	}{
		{"deepseek", "deepseek", &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{"deepseek": {ProviderName: "deepseek", BaseURL: "https://api.deepseek.com/v1", APIKeySet: true, APIKey: "k"}}}},
		{"openrouter", "openrouter", &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{"openrouter": {ProviderName: "openrouter", BaseURL: "https://openrouter.ai/api/v1", APIKeySet: true, APIKey: "k"}}}},
		{"zai", "zai", &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{"zai": {ProviderName: "zai", BaseURL: "https://api.z.ai/api/paas/v4", APIKeySet: true, APIKey: "k"}}}},
		{"ollama-cloud", "ollama", &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{"ollama": {ProviderName: "ollama", BaseURL: "https://ollama.com/v1", APIKeySet: true, APIKey: "k"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comp, err := NewForProvider(tc.res, tc.providerName)
			if err != nil {
				t.Fatalf("NewForProvider: %v", err)
			}
			client := comp.(*OpenAICompat)
			for _, tr := range httpTransportsOf(client) {
				if tr == def {
					t.Fatal("cloud client shares http.DefaultTransport; every client must own a fresh clone")
				}
				if tr.DialContext == nil || reflect.ValueOf(tr.DialContext).Pointer() != reflect.ValueOf(def.DialContext).Pointer() {
					t.Fatal("cloud client transport is pinned; a cloud client is never pinned")
				}
			}
			tr := modalOf(t, client).streamed
			if tr.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
				t.Fatalf("cloud clone ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
			}
		})
	}
}

// TestNewForProviderPinsLoopbackDialForNonOllamaProvider is the regression
// test for the generalized loopback protection: a NON-ollama provider (a
// local llmgateway/OpenAI-compatible server, the reported real-world case)
// on a verified loopback base_url gets the SAME dial-pinning ollama's own
// factory has always had, not the default transport. Before this change,
// only providerName == "ollama" received DialContext; every other provider
// pointed at 127.0.0.1 silently kept http.DefaultTransport, so a resolver
// later answering "localhost" with a non-loopback address (DNS rebinding)
// could route that provider's Bearer token to an address the base_url
// string never admitted to.
func TestNewForProviderPinsLoopbackDialForNonOllamaProvider(t *testing.T) {
	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{
		"llmgateway": {ProviderName: "llmgateway", BaseURL: "http://127.0.0.1:8317/v1", APIKeySet: true, APIKey: "k"},
	}}
	comp, err := NewForProvider(res, "llmgateway")
	if err != nil {
		t.Fatalf("NewForProvider: %v", err)
	}
	client := comp.(*OpenAICompat)
	tr := anyTransport(client)
	if tr == nil {
		t.Fatalf("inner transport = %T, want *http.Transport", innerTransport(client))
	}
	if tr == http.DefaultTransport {
		t.Fatal("non-ollama provider on a verified loopback base_url kept http.DefaultTransport identity - it must get its own pinned dial, like ollama's factory always has")
	}
	if tr.DialContext == nil {
		t.Fatal("expected a pinned DialContext, got nil")
	}
}

// TestAuditCompatBaseRoundTripperIdentity pins the seam:
// compatBaseRoundTripper ALWAYS returns a fresh *http.Transport clone - never
// http.DefaultTransport itself, never a shared instance across calls, never a
// mutation of the global. A nil dial (cloud client) keeps the clone's DEFAULT
// dialer; a non-nil dial (verified loopback client) carries the pinned
// function. Both dial paths carry the response-header bound, and the clone
// quality assertions hold: DialTLSContext stays nil (https dials must use
// DialContext), Proxy and ForceAttemptHTTP2 stay as the default transport set
// them.
func TestAuditCompatBaseRoundTripperIdentity(t *testing.T) {
	def := http.DefaultTransport.(*http.Transport)
	pin := func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, nil }

	unpinnedModal, ok := compatBaseRoundTripper(nil).(*modalHeaderTransport)
	if !ok {
		t.Fatalf("compatBaseRoundTripper(nil) = %T, want *modalHeaderTransport", compatBaseRoundTripper(nil))
	}
	// The generation-phase clone is held to the same clone quality as the
	// streaming one; only its header bound differs, and it carries none
	// because a non-stream header wait is the model working (header_bound.go).
	if unpinnedModal.generation.ResponseHeaderTimeout != 0 {
		t.Fatalf("generation clone ResponseHeaderTimeout = %v, want none", unpinnedModal.generation.ResponseHeaderTimeout)
	}
	if unpinnedModal.generation == def || unpinnedModal.generation.DialTLSContext != nil {
		t.Fatal("generation clone must be a fresh clone with no DialTLSContext")
	}
	unpinned := unpinnedModal.streamed
	if unpinned == def {
		t.Fatal("compatBaseRoundTripper(nil) returned http.DefaultTransport itself; a client must never own the global")
	}
	if unpinned.DialContext == nil || reflect.ValueOf(unpinned.DialContext).Pointer() != reflect.ValueOf(def.DialContext).Pointer() {
		t.Fatal("unpinned (cloud) clone must keep the DEFAULT dialer")
	}
	if unpinned.DialTLSContext != nil {
		t.Fatal("clone must not set DialTLSContext (https dials must use DialContext)")
	}
	if unpinned.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
		t.Fatalf("unpinned clone ResponseHeaderTimeout = %v, want %v", unpinned.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
	}
	if unpinned.ForceAttemptHTTP2 != def.ForceAttemptHTTP2 {
		t.Fatal("clone changed ForceAttemptHTTP2")
	}
	if unpinned.Proxy == nil || reflect.ValueOf(unpinned.Proxy).Pointer() != reflect.ValueOf(def.Proxy).Pointer() {
		t.Fatal("clone changed Proxy")
	}

	pinnedModal, ok := compatBaseRoundTripper(pin).(*modalHeaderTransport)
	if !ok {
		t.Fatalf("compatBaseRoundTripper(pin) = %T, want *modalHeaderTransport", compatBaseRoundTripper(pin))
	}
	// The pin must reach the generation-phase clone too: a non-stream request
	// routes there, and an unpinned dial on that pool would defeat the gate.
	if reflect.ValueOf(pinnedModal.generation.DialContext).Pointer() != reflect.ValueOf(pin).Pointer() {
		t.Fatal("generation clone DialContext is not the pinned function")
	}
	pinned := pinnedModal.streamed
	if pinned == def {
		t.Fatal("pinned base transport must be a clone, not http.DefaultTransport itself")
	}
	if pinned == unpinned {
		t.Fatal("compatBaseRoundTripper must return a fresh transport per call, got a shared instance")
	}
	if reflect.ValueOf(pinned.DialContext).Pointer() != reflect.ValueOf(pin).Pointer() {
		t.Fatal("clone DialContext is not the pinned function")
	}
	if pinned.DialTLSContext != nil {
		t.Fatal("clone must not set DialTLSContext (https dials must use DialContext)")
	}
	if pinned.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
		t.Fatalf("pinned clone ResponseHeaderTimeout = %v, want %v", pinned.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
	}
	if pinned.ForceAttemptHTTP2 != def.ForceAttemptHTTP2 {
		t.Fatal("clone changed ForceAttemptHTTP2")
	}
	if pinned.Proxy == nil || reflect.ValueOf(pinned.Proxy).Pointer() != reflect.ValueOf(def.Proxy).Pointer() {
		t.Fatal("clone changed Proxy")
	}
}

// TestAuditWithOptionsAndRetryThreadsDialContext pins that the custom-retry
// constructor installs the same pinned base transport: no caller can build a
// keyless ollama client with a loose dial through NewOpenAICompatWithOptionsAndRetry.
func TestAuditWithOptionsAndRetryThreadsDialContext(t *testing.T) {
	pin := func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, nil }
	c := NewOpenAICompatWithOptionsAndRetry(CompatOptions{
		Name: "ollama", BaseURL: "http://localhost:11434/v1", APIKey: "", DialContext: pin,
	}, &retryOptions{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond})
	transports := httpTransportsOf(c)
	if len(transports) == 0 {
		t.Fatalf("inner transport = %T, want *http.Transport clones", innerTransport(c))
	}
	for _, tr := range transports {
		if reflect.ValueOf(tr.DialContext).Pointer() != reflect.ValueOf(pin).Pointer() {
			t.Fatal("NewOpenAICompatWithOptionsAndRetry did not thread DialContext")
		}
	}
}

// TestAuditRedirectDialIsPinnedToLoopback proves the pin applies to the
// redirect follow: the base daemon answers with a 307 to a NON-loopback
// address (TEST-NET-3), and the follow-up dial must still land on the pinned
// loopback IP. The target listener sits on the pinned IP; TEST-NET-3 is
// unroutable, so a successful redirect proves the dial was rewritten.
func TestAuditRedirectDialIsPinnedToLoopback(t *testing.T) {
	installLocalhostResolver(t, "127.0.0.2")

	var (
		mu         sync.Mutex
		redirected []string
	)
	targetLn, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.2: %v", err)
	}
	defer targetLn.Close()
	targetPort := portOf(t, targetLn.Addr().String())
	targetSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		redirected = append(redirected, r.Host)
		mu.Unlock()
		writeChatJSON(w, "pong")
	})}
	go targetSrv.Serve(targetLn)
	defer targetSrv.Close()

	daemonLn, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.2: %v", err)
	}
	defer daemonLn.Close()
	daemonPort := portOf(t, daemonLn.Addr().String())
	daemonSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("http://203.0.113.7:%s/chat/completions", targetPort), http.StatusTemporaryRedirect)
	})}
	go daemonSrv.Serve(daemonLn)
	defer daemonSrv.Close()

	comp, err := NewOllama(Options{BaseURL: fmt.Sprintf("http://localhost:%s/v1", daemonPort), APIKey: "sekrit"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	client.client.Timeout = 10 * time.Second

	out, err := client.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("redirect follow failed (pin bypassed?): %v", err)
	}
	if out != "pong" {
		t.Fatalf("out = %q, want pong", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(redirected) != 1 {
		t.Fatalf("redirect target saw %d requests, want 1", len(redirected))
	}
	// The Host header keeps the redirect URL's host; the DIAL must still be
	// pinned to loopback.
	if redirected[0] != fmt.Sprintf("203.0.113.7:%s", targetPort) {
		t.Fatalf("redirect request Host = %q", redirected[0])
	}
}

// TestAuditHTTPSDialIsPinned proves the Clone-based transport keeps the
// pinned DialContext for https too: the clone leaves DialTLSContext nil, so
// every TLS connection is established through the pin, and the TLS SNI stays
// the literal hostname from the base URL.
func TestAuditHTTPSDialIsPinned(t *testing.T) {
	installLocalhostResolver(t, "127.0.0.2")

	var gotServerName string
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotServerName = r.TLS.ServerName
		writeChatJSON(w, "pong")
	}))
	ln, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.2: %v", err)
	}
	ts.Listener = ln
	ts.StartTLS()
	defer ts.Close()
	port := portOf(t, ln.Addr().String())

	comp, err := NewOllama(Options{BaseURL: "https://localhost:" + port + "/v1", APIKey: "sekrit"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	transports := httpTransportsOf(client)
	if len(transports) == 0 {
		t.Fatalf("inner transport = %T, want pinned clones", innerTransport(client))
	}
	for _, tr := range transports {
		if tr.DialTLSContext != nil {
			t.Fatal("pinned clone has DialTLSContext; https dials would bypass the pin")
		}
		// Test-only: trust the httptest self-signed cert so the request
		// completes; the dial path under test is unchanged. Applied to every
		// phase transport, since the request may route to either.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	client.client.Timeout = 10 * time.Second

	out, err := client.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("https chat failed (pin bypassed?): %v", err)
	}
	if out != "pong" {
		t.Fatalf("out = %q, want pong", out)
	}
	if gotServerName != "localhost" {
		t.Fatalf("TLS SNI = %q, want %q", gotServerName, "localhost")
	}
}

// TestAuditLocalhostMultiAddressPinTriesEveryLoopbackIP proves the pinned set
// handles localhost resolving to BOTH 127.0.0.1 and ::1: the dial tries each
// pinned address in turn until one connects.
func TestAuditLocalhostMultiAddressPinTriesEveryLoopbackIP(t *testing.T) {
	orig := lookupLocalhost
	lookupLocalhost = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, nil
	}
	t.Cleanup(func() { lookupLocalhost = orig })

	ln6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("cannot bind [::1]: %v", err)
	}
	defer ln6.Close()
	port := portOf(t, ln6.Addr().String())

	dial, err := newLoopbackDialContext("ollama", "http://localhost:11434/v1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("multi-address pinned dial failed: %v", err)
	}
	defer conn.Close()

	srvConn, err := ln6.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer srvConn.Close()
	_ = srvConn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := srvConn.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("dialed connection did not reach the [::1] listener: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("read %q, want pong", buf)
	}
}

// TestAuditMixedLoopbackAndForeignResolutionFailsClosed proves ONE non-loopback
// answer among loopback answers fails the whole construction closed.
func TestAuditMixedLoopbackAndForeignResolutionFailsClosed(t *testing.T) {
	orig := lookupLocalhost
	lookupLocalhost = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("203.0.113.7")}, nil
	}
	t.Cleanup(func() { lookupLocalhost = orig })

	comp, err := NewOllama(Options{BaseURL: "http://localhost:11434/v1", APIKey: "sekrit"})
	if err == nil {
		t.Fatal("NewOllama must fail closed when any resolved address is non-loopback")
	}
	if comp != nil {
		t.Fatalf("NewOllama returned a completer alongside error %v", err)
	}
	if !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("error = %q, want a non-loopback refusal", err)
	}
}

// TestAuditFailClosedErrorDoesNotLeakTheKey pins the fail-closed surface:
// hostile resolution errors must be actionable (mention loopback / the
// 127.0.0.1 hint), must not be misreported as a missing-key error, and must
// never embed the API key material. The CLI chat path constructs through
// provider.New, so the same error must surface there too.
func TestAuditFailClosedErrorDoesNotLeakTheKey(t *testing.T) {
	installLocalhostResolver(t, "203.0.113.7")
	const secret = "SUPER-SECRET-KEY-123"

	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{
		"ollama": {ProviderName: "ollama", BaseURL: "http://localhost:11434/v1", APIKeyEnv: "OLLAMA_API_KEY", APIKeySet: true, APIKey: secret},
	}}
	comp, err := NewForProvider(res, "ollama")
	if err == nil {
		t.Fatal("NewForProvider must fail closed")
	}
	if comp != nil {
		t.Fatalf("NewForProvider returned a completer alongside error %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("fail-closed error leaked the API key: %q", err)
	}
	if strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("fail-closed error misreported as missing key: %q", err)
	}
	if !strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("fail-closed error is not actionable: %q", err)
	}

	// The exact construction path the CLI chat command uses (provider.New).
	keyless := &config.Resolved{ProviderName: "ollama", ProviderRuntimes: map[string]config.ProviderRuntime{
		"ollama": {ProviderName: "ollama", BaseURL: "http://localhost:11434/v1", APIKeyEnv: "OLLAMA_API_KEY", APIKeySet: false},
	}}
	if _, err := New(keyless); err == nil {
		t.Fatal("provider.New must fail closed for hostile localhost resolution")
	} else if strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("provider.New misreported as missing key: %q", err)
	}
}

type authRecord struct {
	auth    string
	present bool
}

// authRecorder drives a scripted HTTP handler and records every request's
// Authorization header state.
type authRecorder struct {
	mu     sync.Mutex
	seen   []authRecord
	step   int
	script []func(http.ResponseWriter)
}

func newAuthRecorder(script []func(http.ResponseWriter)) *authRecorder {
	return &authRecorder{script: script}
}

func (r *authRecorder) handler(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.seen = append(r.seen, authRecord{auth: req.Header.Get("Authorization")})
	_, r.seen[len(r.seen)-1].present = req.Header["Authorization"]
	s := r.step
	r.step++
	r.mu.Unlock()
	if s >= len(r.script) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	r.script[s](w)
}

func (r *authRecorder) snapshot() []authRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]authRecord, len(r.seen))
	copy(out, r.seen)
	return out
}

// TestAuditKeylessPinnedClientNeverSendsAuthorizationAcrossEveryRequestShape
// pins plan item 2 against the new Clone-based transport: a keyless pinned
// ollama client must not attach an Authorization header on the plain chat
// path, the retry path, the streaming path, the tool-call stream path, or the
// stream-fallback (retryWithoutStreaming) path.
func TestAuditKeylessPinnedClientNeverSendsAuthorizationAcrossEveryRequestShape(t *testing.T) {
	installLocalhostResolver(t, "127.0.0.2")
	rec := newAuthRecorder([]func(http.ResponseWriter){
		// 0: Chat first attempt -> 503 (retryable)
		func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) },
		// 1: Chat retry -> JSON
		func(w http.ResponseWriter) { writeChatJSON(w, "pong") },
		// 2: ChatTurn stream with tools -> SSE
		func(w http.ResponseWriter) { writeSSE(w, sseDelta("hel"), sseDone("lo")) },
		// 3: ChatStream (no tools) -> empty stream, triggers retryWithoutStreaming
		func(w http.ResponseWriter) { writeSSE(w) },
		// 4: retryWithoutStreaming fallback -> JSON
		func(w http.ResponseWriter) { writeChatJSON(w, "fallback") },
	})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(rec.handler))
	ln, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.2: %v", err)
	}
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	port := portOf(t, ln.Addr().String())

	comp, err := NewOllama(Options{BaseURL: "http://localhost:" + port + "/v1", APIKey: "sekrit"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.apiKey != "" {
		t.Fatalf("apiKey = %q, want stripped", client.apiKey)
	}
	client.client.Timeout = 30 * time.Second

	out, err := client.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out != "pong" {
		t.Fatalf("Chat out = %q, want pong", out)
	}

	toolResp, err := client.ChatTurn(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Stream:   true,
		Tools:    []ToolSpec{{"type": "function", "function": map[string]any{"name": "x", "description": "d", "parameters": map[string]any{}}}},
	})
	if err != nil {
		t.Fatalf("ChatTurn stream: %v", err)
	}
	if toolResp.Content != "hello" {
		t.Fatalf("ChatTurn stream content = %q, want hello", toolResp.Content)
	}

	var sb strings.Builder
	streamOut, err := client.ChatStream(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, &sb)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if streamOut != "fallback" || sb.String() != "fallback" {
		t.Fatalf("ChatStream out = %q, writer = %q, want fallback", streamOut, sb.String())
	}

	seen := rec.snapshot()
	if len(seen) != 5 {
		t.Fatalf("server saw %d requests, want 5", len(seen))
	}
	for i, rec := range seen {
		if rec.present {
			t.Fatalf("request %d carried Authorization header %q; keyless pinned client must never send one", i, rec.auth)
		}
	}
}
