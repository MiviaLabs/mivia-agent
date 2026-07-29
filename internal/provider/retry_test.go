package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- RetryRoundTripper unit tests ---

func TestRetryRoundTripper_SuccessNoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected 1 call, got %d", n)
	}
}

func TestRetryRoundTripper_RetryOn429ThenSucceed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Header().Set("Retry-After", "0")
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"finally ok"}}]}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("expected 3 calls, got %d", n)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "finally ok") {
		t.Fatalf("body=%s", body)
	}
}

func TestRetryRoundTripper_RetryOn503(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"overloaded"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok after 503"}}]}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 calls, got %d", n)
	}
}

func TestRetryRoundTripper_NoRetryOn400(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request"}}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected 1 call (no retry on 400), got %d", n)
	}
}

func TestRetryRoundTripper_ExhaustRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"always down"}}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err) // Should return the last 503 response, not error
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	// MaxRetries=2 means: attempt 0, retry 1, retry 2 = 3 total calls
	if n := calls.Load(); n != 3 {
		t.Fatalf("expected 3 calls (2 retries exhausted), got %d", n)
	}
}

func TestRetryRoundTripper_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected context error")
	}
	if err != context.DeadlineExceeded && !strings.Contains(err.Error(), "context") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRetryRoundTripper_RetryAfterHeader(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok after retry-after"}}]}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 calls, got %d", n)
	}
}

func TestRetryRoundTripper_NoRetryOn401(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected 1 call (no retry on 401), got %d", n)
	}
}

func TestRetryRoundTripper_RetryOn502(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok after 502"}}]}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 calls, got %d", n)
	}
}

func TestRetryRoundTripper_RetryOn504(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok after 504"}}]}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 calls, got %d", n)
	}
}

func TestRetryRoundTripper_RetryOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok after 500"}}]}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 calls, got %d", n)
	}
}

// --- Integration: retry wired into OpenAICompat ChatTurn ---

func TestOpenAICompat_ChatTurnRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Header().Set("Retry-After", "0")
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "retry worked"}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptionsAndRetry(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "fake-key"}, &retryOptions{
		MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
	})
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "retry worked" {
		t.Fatalf("got %q", resp.Content)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("expected 3 calls, got %d", n)
	}
}

func TestOpenAICompat_ChatTurnNoRetryOn401(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptionsAndRetry(CompatOptions{Name: "deepseek", BaseURL: srv.URL, APIKey: "bad-key"}, &retryOptions{
		MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
	})
	_, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("expected auth error, got: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected 1 call (no retry on 401), got %d", n)
	}
}

func TestOpenAICompat_ChatTurnRetryThenFail(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"down"}}`)
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptionsAndRetry(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"}, &retryOptions{
		MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
	})
	_, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error after retry exhaustion")
	}
	// 1 initial + 2 retries = 3 calls
	if n := calls.Load(); n != 3 {
		t.Fatalf("expected 3 calls, got %d", n)
	}
}

func TestOpenAICompat_ChatStreamWithToolsRetriesStream(t *testing.T) {
	// When tools are present, ChatStream uses streaming ChatTurn (retry on 429).
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Header().Set("Retry-After", "0")
			_, _ = io.WriteString(w, `{}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tool-retry-ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptionsAndRetry(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"}, &retryOptions{
		MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
	})
	var buf strings.Builder
	out, err := c.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolSpec{{"type": "function"}},
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if out != "tool-retry-ok" {
		t.Fatalf("got %q", out)
	}
	if buf.String() != "tool-retry-ok" {
		t.Fatalf("StreamWriter got %q", buf.String())
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 calls, got %d", n)
	}
}

// --- parseRetryAfter tests ---

func TestParseRetryAfter_Seconds(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Retry-After": {"5"}}}
	d := parseRetryAfter(resp)
	if d != 5*time.Second {
		t.Fatalf("got %v", d)
	}
}

func TestParseRetryAfter_Zero(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Retry-After": {"0"}}}
	d := parseRetryAfter(resp)
	if d != 0 {
		t.Fatalf("got %v", d)
	}
}

func TestParseRetryAfter_Empty(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	d := parseRetryAfter(resp)
	if d != 0 {
		t.Fatalf("got %v", d)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	// A date 1 second in the future.
	future := time.Now().Add(time.Second).Format(time.RFC1123)
	resp := &http.Response{Header: http.Header{"Retry-After": {future}}}
	d := parseRetryAfter(resp)
	if d <= 0 || d > 5*time.Second {
		t.Fatalf("unexpected duration %v", d)
	}
}

// --- isRetryableTransportError tests ---

func TestIsRetryableTransportError(t *testing.T) {
	cases := []struct {
		err       error
		retryable bool
	}{
		{fmt.Errorf("connection refused"), true},
		{fmt.Errorf("no such host"), true},
		{fmt.Errorf("connection reset by peer"), true},
		{fmt.Errorf("tls handshake timeout"), true},
		{fmt.Errorf("dial tcp 1.2.3.4:443: i/o timeout"), true},
		{fmt.Errorf("broken pipe"), true},
		{fmt.Errorf("EOF"), true},
		{fmt.Errorf("something else"), false},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{nil, false},
	}
	for _, c := range cases {
		got := isRetryableTransportError(c.err)
		if got != c.retryable {
			t.Errorf("isRetryableTransportError(%v) = %v, want %v", c.err, got, c.retryable)
		}
	}
}

// --- Default retry config is applied correctly ---

func TestDefaultRetryOptionsAreSensible(t *testing.T) {
	opts := defaultRetryOptions()
	if opts.MaxRetries != 3 {
		t.Fatalf("MaxRetries=%d", opts.MaxRetries)
	}
	if opts.BaseDelay <= 0 {
		t.Fatalf("BaseDelay=%v", opts.BaseDelay)
	}
	if opts.MaxDelay <= opts.BaseDelay {
		t.Fatalf("MaxDelay=%v <= BaseDelay=%v", opts.MaxDelay, opts.BaseDelay)
	}
}

func TestNewRetryRoundTripperDefaults(t *testing.T) {
	rt := newRetryRoundTripper(nil, retryOptions{})
	if rt == nil {
		t.Fatal("nil tripper")
	}
	if rt.opts.MaxRetries != 3 {
		t.Fatalf("MaxRetries=%d", rt.opts.MaxRetries)
	}
	if rt.opts.BaseDelay <= 0 {
		t.Fatalf("BaseDelay=%v", rt.opts.BaseDelay)
	}
	if rt.inner == nil {
		t.Fatal("inner transport is nil")
	}
}

func TestRetryRoundTripper_NewOpenAICompatUsesRetry(t *testing.T) {
	// Verify that NewOpenAICompat (the default constructor) sets up retry.
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "https://example.com", APIKey: "key"})
	if c == nil {
		t.Fatal("nil client")
	}
	if c.client == nil {
		t.Fatal("nil http client")
	}
	// The transport should be a retryRoundTripper.
	rt, ok := c.client.Transport.(*retryRoundTripper)
	if !ok {
		t.Fatalf("expected retryRoundTripper, got %T", c.client.Transport)
	}
	if rt.opts.MaxRetries != 3 {
		t.Fatalf("expected 3 retries, got %d", rt.opts.MaxRetries)
	}
}

// --- Retry on network-level failure (connection refused) ---

func TestRetryRoundTripper_ConnectionRefused(t *testing.T) {
	// Use a closed port to simulate connection refused.
	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{
		MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
	})
	client := &http.Client{Transport: rt, Timeout: 2 * time.Second}

	req, _ := http.NewRequest("GET", "http://127.0.0.1:1", nil)
	_, err := client.Do(req)
	// Should get a connection error (not infinite hang).
	if err == nil {
		t.Fatal("expected connection error")
	}
	// Verify it's a network/transport error, not a nil panic.
	if !strings.Contains(err.Error(), "connection refused") &&
		!strings.Contains(err.Error(), "connect:") &&
		!strings.Contains(err.Error(), "no route") {
		t.Logf("got expected network error: %v", err)
	}
}
