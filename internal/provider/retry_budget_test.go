package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- Five-attempt transport budget ---

// The production budget is five attempts per outbound transport exchange: the
// initial request plus four retries. A rate limit that clears only on the
// provider's fifth look must still return the answer instead of surfacing a
// 429 the caller cannot act on.
func TestRetryRoundTripper_SucceedsOnFifthAttempt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n := calls.Add(1); n <= 4 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"fifth time"}}]}`)
	}))
	defer srv.Close()

	// MaxRetries stays zero so the constructor supplies the production default;
	// only the delays are shortened. This asserts the shipped budget, not a
	// number the test picked for itself.
	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond})
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
	if n := calls.Load(); n != 5 {
		t.Fatalf("expected 5 calls, got %d", n)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "fifth time") {
		t.Fatalf("body=%s", body)
	}
}

// The budget is also a ceiling. A provider that keeps refusing gets five
// attempts and no more, and the caller receives the last real response rather
// than a synthesised error.
func TestRetryRoundTripper_StopsAfterFiveAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"still rate limited"}}`)
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport, retryOptions{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if n := calls.Load(); n != 5 {
		t.Fatalf("expected 5 calls, got %d", n)
	}
}

// countingTransport answers every call with the same scripted status, so a
// retry test never depends on a real socket or a real clock.
type countingTransport struct {
	calls  atomic.Int32
	status int
	header http.Header
	body   string
	// err, when set, is returned instead of a response.
	err error
	// observe, when set, runs after each call is counted.
	observe func(call int32)
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := c.calls.Add(1)
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		req.Body.Close()
	}
	if c.observe != nil {
		c.observe(n)
	}
	if c.err != nil {
		return nil, c.err
	}
	header := c.header.Clone()
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: c.status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Request:    req,
	}, nil
}

// A request whose body cannot be rewound must not be replayed: GetBody is the
// only way to produce the bytes a second time, and calling a nil one panics
// inside the transport, which takes the process down. Fail the exchange with a
// controlled error instead, before a second request reaches the provider.
func TestRetryRoundTripper_NonReplayableBodyReturnsError(t *testing.T) {
	inner := &countingTransport{status: http.StatusTooManyRequests, body: `{"error":{"message":"rate limited"}}`}
	rt := newRetryRoundTripper(inner, retryOptions{MaxRetries: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond})

	req, err := http.NewRequest(http.MethodPost, "http://example.invalid/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A body from an arbitrary reader: net/http leaves GetBody nil for anything
	// it cannot re-open, and callers may attach one directly.
	req.Body = io.NopCloser(strings.NewReader(`{"model":"m"}`))
	req.GetBody = nil
	req.ContentLength = -1

	resp, err := rt.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected a controlled error for a non-replayable body")
	}
	if !strings.Contains(err.Error(), "rewind") {
		t.Fatalf("error should name the rewind failure, got: %v", err)
	}
	if n := inner.calls.Load(); n != 1 {
		t.Fatalf("expected 1 call, got %d", n)
	}
}

// trackedBody reports whether it was closed.
type trackedBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackedBody) Close() error {
	b.closed.Store(true)
	return nil
}

// The body for the next attempt is staged before the backoff wait so a
// non-replayable request fails fast. If the wait is then cut short, nothing
// downstream will ever see that body - GetBody may hand back a real handle, and
// abandoning it leaks it for the life of the process.
func TestRetryRoundTripper_CancelDuringBackoffClosesStagedBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := &countingTransport{
		status:  http.StatusTooManyRequests,
		body:    `{"error":{"message":"rate limited"}}`,
		observe: func(int32) { cancel() },
	}
	rt := newRetryRoundTripper(inner, retryOptions{MaxRetries: 4, BaseDelay: time.Minute, MaxDelay: 2 * time.Minute})

	staged := &trackedBody{Reader: strings.NewReader(`{"model":"m"}`)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid/v1", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = func() (io.ReadCloser, error) { return staged, nil }

	if _, err := rt.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancel, got: %v", err)
	}
	if !staged.closed.Load() {
		t.Fatal("the staged retry body was abandoned unclosed")
	}
}

// Every retry must carry the same bytes and the same request key: a mutated
// body would ask a different question on the retry, and a fresh key defeats
// any upstream that dedupes on it, so one turn gets billed twice. Separate
// logical requests must still get separate keys, or a provider could suppress
// a genuinely new request as a duplicate.
func TestRetryRoundTripper_PreservesBodyAndIdempotencyKey(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
		keys   []string
	)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		if n := calls.Add(1); n <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"},
			},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptionsAndRetry(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"}, &retryOptions{
		MaxRetries: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
	})
	turn := Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	if _, err := c.ChatTurn(context.Background(), turn); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	firstBodies := append([]string(nil), bodies...)
	firstKeys := append([]string(nil), keys...)
	mu.Unlock()

	if len(firstBodies) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(firstBodies))
	}
	for i := 1; i < len(firstBodies); i++ {
		if firstBodies[i] != firstBodies[0] {
			t.Fatalf("retry %d sent different bytes:\n got %q\nwant %q", i, firstBodies[i], firstBodies[0])
		}
		if firstKeys[i] != firstKeys[0] {
			t.Fatalf("retry %d sent a different idempotency key: %q != %q", i, firstKeys[i], firstKeys[0])
		}
	}
	if firstKeys[0] == "" {
		t.Fatal("no idempotency key was sent")
	}

	// A second logical request is not a retry: it must be distinguishable.
	calls.Store(2) // the next call answers 200 immediately
	if _, err := c.ChatTurn(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	lastKey := keys[len(keys)-1]
	mu.Unlock()
	if lastKey == firstKeys[0] {
		t.Fatalf("a separate request reused the retry key %q", lastKey)
	}
}

// --- Cumulative wall-clock retry budget (TotalBudget) ---

// The attempt cap alone did not bound wall-clock time: every retry got a
// fresh http.Client window, so one logical call could stretch for five of
// them. TotalBudget is a cumulative retry-GRANT gate measured from RoundTrip
// start; it never preempts an in-flight attempt, and the final transport
// error reaches the caller unwrapped.

// scriptedRetryTransport sleeps sleeps[i] before answering attempt i+1, so a
// test drives the budget deterministically and never sleeps toward the
// 5-minute default. err, when set, is the transport error every attempt
// returns; otherwise status is the scripted HTTP status.
type scriptedRetryTransport struct {
	sleeps []time.Duration
	err    error
	status int
	calls  atomic.Int32
}

func (s *scriptedRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := s.calls.Add(1)
	if i := int(n) - 1; i < len(s.sleeps) && s.sleeps[i] > 0 {
		time.Sleep(s.sleeps[i])
	}
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		req.Body.Close()
	}
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{
		StatusCode: s.status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// totalBudgetCase is one row of the TotalBudget table.
type totalBudgetCase struct {
	name         string
	budget       time.Duration
	sleeps       []time.Duration
	err          error
	status       int
	wantAttempts int32
	wantErr      bool
	wantSameErr  bool
}

func TestRetryRoundTripper_TotalBudget(t *testing.T) {
	sentinel := errors.New("transport stalled before response headers")
	cases := []totalBudgetCase{
		{
			// The first attempt alone blocks past the whole budget: no
			// retry is granted after it, and the transport error is
			// returned unwrapped.
			name:         "slow_first_attempt_exhausts_budget_no_retry",
			budget:       50 * time.Millisecond,
			sleeps:       []time.Duration{60 * time.Millisecond},
			err:          sentinel,
			wantAttempts: 1,
			wantErr:      true,
			wantSameErr:  true,
		},
		{
			// Attempt 1 is fast, so it earns a retry; attempt 2 spends the
			// budget, so the gate closes after it. The in-flight attempt is
			// never preempted.
			name:         "budget_spent_mid_sequence_stops_later_retries",
			budget:       50 * time.Millisecond,
			sleeps:       []time.Duration{0, 60 * time.Millisecond},
			err:          sentinel,
			wantAttempts: 2,
			wantErr:      true,
			wantSameErr:  true,
		},
		{
			// Instant transport failures spend almost no budget: the
			// attempt cap, not the clock, ends the sequence.
			name:         "instant_failures_reach_all_five_attempts",
			budget:       50 * time.Millisecond,
			sleeps:       []time.Duration{0, 0, 0, 0, 0},
			err:          errors.New("dial tcp: connection refused"),
			wantAttempts: 5,
			wantErr:      true,
		},
		{
			// An explicit budget is honored on the success path too: one
			// 200 response succeeds whatever the budget says.
			name:         "first_attempt_success_ignores_budget",
			budget:       time.Nanosecond,
			sleeps:       []time.Duration{0},
			status:       http.StatusOK,
			wantAttempts: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runTotalBudgetCase(t, tc, sentinel)
		})
	}
}

// runTotalBudgetCase drives one TotalBudget table row: it rounds the
// scripted transport through the retry wrapper and checks the attempt
// count, the error surface, and (when required) that the final error is the
// transport error itself, unwrapped.
func runTotalBudgetCase(t *testing.T, tc totalBudgetCase, sentinel error) {
	t.Helper()
	inner := &scriptedRetryTransport{sleeps: tc.sleeps, err: tc.err, status: tc.status}
	rt := newRetryRoundTripper(inner, retryOptions{
		MaxRetries:  4,
		BaseDelay:   time.Millisecond,
		MaxDelay:    2 * time.Millisecond,
		TotalBudget: tc.budget,
	})
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if tc.wantErr {
		if err == nil {
			if resp != nil {
				resp.Body.Close()
			}
			t.Fatal("want the transport error, got a response")
		}
	} else if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
	if got := inner.calls.Load(); got != tc.wantAttempts {
		t.Fatalf("attempts = %d, want %d", got, tc.wantAttempts)
	}
	if tc.wantSameErr && err != sentinel {
		t.Fatalf("last error = %v, want the transport error returned unwrapped", err)
	}
}

// The shipped defaults: a 5-minute cumulative budget, and a zero TotalBudget
// in a hand-built retryOptions selects that default instead of disabling the
// gate.
func TestRetryRoundTripper_TotalBudgetDefault(t *testing.T) {
	if got := defaultRetryOptions().TotalBudget; got != 5*time.Minute {
		t.Fatalf("defaultRetryOptions().TotalBudget = %v, want 5m", got)
	}
	rt := newRetryRoundTripper(nil, retryOptions{})
	if rt.opts.TotalBudget != 5*time.Minute {
		t.Fatalf("newRetryRoundTripper zero budget resolved to %v, want 5m", rt.opts.TotalBudget)
	}
}
