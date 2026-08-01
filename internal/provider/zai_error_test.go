package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// z.ai reports the same failure in several wire shapes: the documented flat
// {"code":<int>,"message":...}, a flat form with the code as a string, and an
// OpenAI-style {"error":{"code":"<string>",...}} envelope. The numeric code is
// the only diagnostic the caller gets - the provider's message is deliberately
// never forwarded - so losing it to a shape mismatch leaves a bare HTTP status
// and no way to tell a quota block from an unknown model.
func TestZAIErrorParserExtractsCodeFromEveryWireShape(t *testing.T) {
	for name, body := range map[string]string{
		"flat int code":      `{"code":1113,"message":"m"}`,
		"flat string code":   `{"code":"1113","message":"m"}`,
		"nested string code": `{"error":{"code":"1113","message":"m"}}`,
		"nested int code":    `{"error":{"code":1113,"message":"m"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := zaiErrorParser(http.StatusTooManyRequests, []byte(body))
			if err == nil || !strings.Contains(err.Error(), "1113") {
				t.Fatalf("code not reported: %v", err)
			}
		})
	}
}

// A bare code is not actionable to anyone without the published error table.
func TestZAIErrorParserExplainsKnownCodes(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   int
		want   string
	}{
		{http.StatusTooManyRequests, 1113, "balance"},
		{http.StatusTooManyRequests, 1311, "subscription"},
		{http.StatusBadRequest, 1211, "unknown model"},
		{http.StatusBadRequest, 1212, "call method"},
	} {
		body := `{"error":{"code":"` + strconv.Itoa(tc.code) + `","message":"m"}}`
		err := zaiErrorParser(tc.status, []byte(body))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("code %d: want %q in error, got %v", tc.code, tc.want, err)
		}
	}
}

// The provider echoes request content back in error messages, so the message is
// never forwarded whatever shape carries it.
func TestZAIErrorParserNeverEchoesProviderMessage(t *testing.T) {
	const secret = "zai-secret-key"
	const prompt = "private prompt"
	for _, body := range []string{
		`{"code":1113,"message":"` + secret + ` ` + prompt + `"}`,
		`{"code":"1113","message":"` + secret + ` ` + prompt + `"}`,
		`{"error":{"code":"1113","message":"` + secret + ` ` + prompt + `"}}`,
	} {
		err := zaiErrorParser(http.StatusTooManyRequests, []byte(body))
		if err == nil {
			t.Fatalf("body %q: expected an error", body)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), prompt) {
			t.Fatalf("provider message leaked: %v", err)
		}
	}
}

// The parser runs on every SSE chunk with status 200. A chunk that carries no
// error signal must not be turned into one.
func TestZAIErrorParserPassesCleanPayloads(t *testing.T) {
	for _, body := range []string{
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`{"id":"x","choices":[],"usage":{"total_tokens":5}}`,
		`{"id":"x","created":1730000000}`,
	} {
		if err := zaiErrorParser(http.StatusOK, []byte(body)); err != nil {
			t.Fatalf("clean payload %q rejected: %v", body, err)
		}
	}
}

// Quota and plan states arrive as HTTP 429 but are not transient: an insufficient
// balance does not refill during a 5-second backoff. Retrying spends three more
// requests per turn and delays the real error.
func TestZAIQuotaHTTP429IsNotRetried(t *testing.T) {
	for _, code := range []int{1113, 1308, 1309, 1310, 1311, 1314} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":"`+strconv.Itoa(code)+`","message":"m"}}`)
		}))
		comp, err := NewZAI(Options{BaseURL: srv.URL, APIKey: "k"})
		if err != nil {
			srv.Close()
			t.Fatal(err)
		}
		_, err = comp.Chat(context.Background(), Request{Model: "glm-5.2", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
		srv.Close()
		if err == nil {
			t.Fatalf("code %d: expected an error", code)
		}
		if calls != 1 {
			t.Fatalf("code %d: retried a permanent quota error (%d calls)", code, calls)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(code)) {
			t.Fatalf("code %d: not reported: %v", code, err)
		}
	}
}

// A genuine rate limit or an overloaded service still earns a retry.
func TestZAITransientHTTP429IsRetried(t *testing.T) {
	for _, code := range []int{1302, 1305} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls > 1 {
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
				return
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":"`+strconv.Itoa(code)+`","message":"m"}}`)
		}))
		comp, err := NewZAI(Options{BaseURL: srv.URL, APIKey: "k"})
		if err != nil {
			srv.Close()
			t.Fatal(err)
		}
		text, err := comp.Chat(context.Background(), Request{Model: "glm-5.2", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
		srv.Close()
		if err != nil || text != "ok" {
			t.Fatalf("code %d: text=%q err=%v", code, text, err)
		}
		if calls < 2 {
			t.Fatalf("code %d: transient 429 was not retried", code)
		}
	}
}

// An unrecognised 429 keeps the shared status-code policy: retry.
func TestZAIUnknownHTTP429KeepsDefaultRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"detail":"no code here"}`)
	}))
	defer srv.Close()
	comp, err := NewZAI(Options{BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := comp.Chat(context.Background(), Request{Model: "glm-5.2", Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err == nil {
		t.Fatal("expected an error")
	}
	if calls < 2 {
		t.Fatalf("unknown 429 was not retried (%d calls)", calls)
	}
}

// The classifier peeks at the response body to read the code. The body the
// caller reads afterwards must still be complete, or the error text is built
// from a truncated payload.
func TestRetryClassifierLeavesResponseBodyIntact(t *testing.T) {
	const payload = `{"error":{"code":"1113","message":"the full body must survive the peek"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()
	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{
		MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
		NonRetryable: func(int, []byte) bool { return true },
	})
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("body corrupted by peek:\n got %q\nwant %q", got, payload)
	}
}

// Retry-After is clamped to MaxDelay so one 429 cannot park the CLI. Retrying
// at the cap when the server asked for longer is guaranteed-fail traffic against
// an account that is already rate limited, so give up instead.
func TestRetryStopsWhenRetryAfterExceedsMaxDelay(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{
		MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
	})
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if calls != 1 {
		t.Fatalf("retried inside a window it cannot wait out (%d calls)", calls)
	}
}
