package provider

// Round-10 regression tests for the round-9 confirmed finding: the exported
// NewOllama constructor must fail closed (mirroring NewForProvider) when the
// base_url is NOT a verified loopback address and no non-blank API key is
// provided. A keyless ollama client is constructible iff
// config.IsOllamaLoopback(base_url) - at every entry point, including this
// exported one - so keyless traffic can never leave the machine (plan §12).

import (
	"context"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestRound10NewOllamaFailClosedForCloudKeyless(t *testing.T) {
	cases := []struct {
		name    string
		options Options
	}{
		{"default base (ollama.com cloud)", Options{}},
		{"https cloud", Options{BaseURL: "https://ollama.com/v1"}},
		{"non-loopback LAN", Options{BaseURL: "http://192.168.1.2:11434/v1"}},
		{"cloud with whitespace key", Options{BaseURL: "https://ollama.com/v1", APIKey: " \t "}},
		{"https with whitespace key", Options{BaseURL: "https://example.test/v1", APIKey: "   "}},
		{"trailing-dot host", Options{BaseURL: "http://localhost.:11434/v1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comp, err := NewOllama(tc.options)
			if err == nil {
				comp.(*OpenAICompat).client.CloseIdleConnections()
				t.Fatalf("NewOllama(%+v) succeeded without a key at a non-loopback base_url, want fail-closed", tc.options)
			}
			if !strings.Contains(err.Error(), "missing API key") {
				t.Fatalf("NewOllama(%+v) error = %v, want the documented missing-key gate", tc.options, err)
			}
		})
	}
}

// TestRound10NewOllamaKeylessLoopbackStillPinned is the positive control for
// the gate above: keyless construction stays legal for verified loopback
// base_urls and must still install the pinned dial.
func TestRound10NewOllamaKeylessLoopbackStillPinned(t *testing.T) {
	comp, err := NewOllama(Options{BaseURL: "http://127.0.0.1:11434/v1"})
	if err != nil {
		t.Fatalf("NewOllama(loopback) = %v, want success", err)
	}
	client := comp.(*OpenAICompat)
	tr, ok := innerTransport(client).(*http.Transport)
	if !ok || tr.DialContext == nil {
		t.Fatal("loopback keyless client lost its pinned dial")
	}
	client.client.CloseIdleConnections()
}

// TestRound10NewOllamaCloudKeyedUnchanged proves keyed cloud construction is
// unaffected by the loopback gate: it succeeds, keeps the key, and gets its
// own fresh transport clone whose dialer stays the DEFAULT one - a cloud
// client is never pinned. The clone carries the response-header bound like
// every client transport, but no other setting changes.
func TestRound10NewOllamaCloudKeyedUnchanged(t *testing.T) {
	comp, err := NewOllama(Options{BaseURL: "https://ollama.com/v1", APIKey: "sekrit"})
	if err != nil {
		t.Fatalf("NewOllama(cloud, key) = %v, want success", err)
	}
	client := comp.(*OpenAICompat)
	if client.apiKey != "sekrit" {
		t.Fatalf("apiKey = %q, want %q", client.apiKey, "sekrit")
	}
	tr, ok := innerTransport(client).(*http.Transport)
	if !ok {
		t.Fatal("cloud keyed client missing transport")
	}
	def := http.DefaultTransport.(*http.Transport)
	if tr == def {
		t.Fatal("cloud keyed client shares http.DefaultTransport; every client must own a fresh clone")
	}
	if tr.DialContext == nil || reflect.ValueOf(tr.DialContext).Pointer() != reflect.ValueOf(def.DialContext).Pointer() {
		t.Fatal("cloud keyed client transport is pinned; the pin is loopback-only, the clone must keep the DEFAULT dialer")
	}
	if tr.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
		t.Fatalf("cloud clone ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
	}
	client.client.CloseIdleConnections()
}

// TestRound10NewOllamaGateMatchesNewForProvider pins the two entry points to
// the same decision for the same (base_url, key) input, so a divergence can
// never re-open the round-9 escape.
func TestRound10NewOllamaGateMatchesNewForProvider(t *testing.T) {
	inputs := []struct {
		baseURL string
		key     string
	}{
		{"http://127.0.0.1:11434/v1", ""},
		{"http://localhost:11434/v1", ""},
		{"https://ollama.com/v1", ""},
		{"https://ollama.com/v1", "k"},
		{"http://192.168.1.2:11434/v1", ""},
	}
	for _, in := range inputs {
		res := &config.Resolved{
			ProviderName: "ollama",
			ProviderRuntimes: map[string]config.ProviderRuntime{
				"ollama": {ProviderName: "ollama", BaseURL: in.baseURL, APIKeyEnv: "TEST_KEY", APIKeySet: in.key != "", APIKey: in.key},
			},
		}
		_, fpErr := NewForProvider(res, "ollama")
		_, ctorErr := NewOllama(Options{BaseURL: in.baseURL, APIKey: in.key})
		if (fpErr == nil) != (ctorErr == nil) {
			t.Fatalf("gate divergence for base_url %q key %q: NewForProvider err=%v, NewOllama err=%v", in.baseURL, in.key, fpErr, ctorErr)
		}
	}
}

// TestRound10PinnedDialConnectsToLoopbackOnly proves the pinned dial rewrites
// an off-loopback target (10.255.255.1, reserved doc range) to a verified
// loopback address: the CONNECTION's remote address must be loopback.
func TestRound10PinnedDialConnectsToLoopbackOnly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acceptAndClose(ln)
	port := ln.Addr().(*net.TCPAddr).Port

	comp, err := NewOllama(Options{BaseURL: "http://127.0.0.1:11434/v1"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	tr, ok := innerTransport(client).(*http.Transport)
	if !ok || tr.DialContext == nil {
		t.Fatal("loopback keyless client lost its pinned dial")
	}
	conn, err := tr.DialContext(context.Background(), "tcp", net.JoinHostPort("10.255.255.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("pinned dial to 10.255.255.1:%d failed: %v (want rewrite to loopback and CONNECT)", port, err)
	}
	defer conn.Close()
	remoteHost, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(remoteHost)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("pinned dial connected to NON-loopback remote %s; want loopback", conn.RemoteAddr())
	}
	client.client.CloseIdleConnections()
}
