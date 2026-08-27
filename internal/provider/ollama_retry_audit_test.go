package provider

// Hostile audit (0dccb870..HEAD) of the ollama retry/backoff interplay:
// keyless loopback requests must fail fast on 404 (no retry), bounded on
// connection-refused, and must NEVER gain an Authorization header on a retry.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countRoundTripper counts attempts and records whether any request carried an
// Authorization header. It wraps the INNER transport so retries pass through it.
func countRoundTripper(inner http.RoundTripper, attempts *atomic.Int32, sawAuth *atomic.Bool) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts.Add(1)
		if h := req.Header.Get("Authorization"); h != "" {
			sawAuth.Store(true)
		}
		return inner.RoundTrip(req)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// A keyless ollama client pointed at a loopback 404 server must send exactly
// ONE request (404 is not retryable) with no Authorization header, and the
// surfaced error must be a provider 404 error, not a missing-key error.
func TestAuditOllamaKeylessLoopback404SingleAttemptNoAuth(t *testing.T) {
	var attempts atomic.Int32
	var sawAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	comp, err := NewOllama(Options{BaseURL: srv.URL, APIKey: "sekrit"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.apiKey != "" {
		t.Fatalf("NewOllama kept apiKey %q for a loopback base URL", client.apiKey)
	}
	client.client = &http.Client{
		Timeout:   time.Minute,
		Transport: newRetryRoundTripper(countRoundTripper(http.DefaultTransport, &attempts, &sawAuth), defaultRetryOptions()),
	}

	_, err = client.Chat(context.Background(), Request{Model: "gpt-oss:120b", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("expected provider 404 error, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %q, want a provider 404 error", err)
	}
	if strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("error = %q: key gate must not fire for ollama loopback", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want exactly 1 (404 must not be retried)", got)
	}
	if sawAuth.Load() {
		t.Fatal("keyless ollama request carried an Authorization header")
	}
}

// A keyless ollama client against a retryable 503 must retry (bounded) and
// must never gain an Authorization header on the retried request: setHeaders
// runs once at request-build time and the transport replays the same request.
func TestAuditOllamaKeylessRetryNeverGainsAuthorization(t *testing.T) {
	var attempts atomic.Int32
	var sawAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Load() == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer srv.Close()

	comp, err := NewOllama(Options{BaseURL: srv.URL, APIKey: "sekrit"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.apiKey != "" {
		t.Fatalf("NewOllama kept apiKey %q for a loopback base URL", client.apiKey)
	}
	client.client = &http.Client{
		Timeout:   time.Minute,
		Transport: newRetryRoundTripper(countRoundTripper(http.DefaultTransport, &attempts, &sawAuth), retryOptions{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
	}

	out, err := client.Chat(context.Background(), Request{Model: "gpt-oss:120b", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "pong" {
		t.Fatalf("out = %q, want pong", out)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2 (503 + retry)", got)
	}
	if sawAuth.Load() {
		t.Fatal("keyless ollama retry gained an Authorization header")
	}
}

// Control: a KEYED client retried across a 503 must carry the same Bearer
// header on every attempt (headers are preserved, not dropped). The keyed
// client is built directly (NewOllama strips keys for loopback by design).
func TestAuditKeyedRetryKeepsAuthorization(t *testing.T) {
	var attempts atomic.Int32
	var sawAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Load() == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer srv.Close()

	client := NewOpenAICompatWithOptions(CompatOptions{Name: "ollama", BaseURL: srv.URL, APIKey: "real-key-123"})
	if client.apiKey != "real-key-123" {
		t.Fatalf("apiKey = %q, want real-key-123", client.apiKey)
	}
	client.client = &http.Client{
		Timeout:   time.Minute,
		Transport: newRetryRoundTripper(countRoundTripper(http.DefaultTransport, &attempts, &sawAuth), retryOptions{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
	}

	if _, err := client.Chat(context.Background(), Request{Model: "gpt-oss:120b", Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2 (503 + retry)", got)
	}
	if !sawAuth.Load() {
		t.Fatal("keyed client request carried no Authorization header on any attempt")
	}
}

// Connection-refused against a closed loopback port must fail fast and stay
// bounded: the production default budget (4 retries, 200ms base backoff)
// yields exactly 5 attempts and finishes well under any unbounded hang.
func TestAuditOllamaLoopbackConnectionRefusedBounded(t *testing.T) {
	// Grab a port that is guaranteed closed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	ln.Close()

	comp, err := NewOllama(Options{BaseURL: "http://" + closedAddr + "/v1", APIKey: "sekrit"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	var attempts atomic.Int32
	client.client = &http.Client{
		Timeout:   time.Minute,
		Transport: newRetryRoundTripper(countRoundTripper(http.DefaultTransport, &attempts, new(atomic.Bool)), defaultRetryOptions()),
	}

	start := time.Now()
	_, err = client.Chat(context.Background(), Request{Model: "gpt-oss:120b", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a connection error, got nil")
	}
	if !wantDialRefused(err) {
		t.Fatalf("error = %q, want a connection-refused error", err)
	}
	if got := attempts.Load(); got != 5 {
		t.Fatalf("attempts = %d, want 5 (1 initial + 4 retries)", got)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("connection-refused failure took %v - unbounded retry?", elapsed)
	}
	t.Logf("connection-refused surfaced in %v after %d attempts", elapsed, attempts.Load())
}

// Record the exact surface error text for keyless ollama against a loopback
// 404 server (what a user sees instead of a missing-key error).
func TestAuditOllama404ErrorTextForRecord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	comp, err := NewOllama(Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	_, err = client.Chat(context.Background(), Request{Model: "gpt-oss:120b", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	t.Logf("surface error for keyless ollama against loopback 404: %v", err)
}
