package provider

// The retry budget exists to absorb transport faults. A response-header
// timeout is one, and before this it was silently exempt: it reports
// errors.Is(err, context.DeadlineExceeded), which the cancellation guard read
// as "the caller stopped this call", so the request that most needed a second
// attempt got none.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Integration: a real transport, a real header bound, a server that withholds
// headers once. The exchange must survive on the retry.
func TestRetryRoundTripper_RetriesResponseHeaderTimeout(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			// Outlast the header bound below, then answer nobody is reading.
			time.Sleep(300 * time.Millisecond)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"second look"}}]}`)
	}))
	defer srv.Close()

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = 50 * time.Millisecond
	rt := newRetryRoundTripper(base, retryOptions{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond})
	client := &http.Client{Transport: rt, Timeout: 10 * time.Second}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("a header timeout must earn a retry, got err=%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "second look") {
		t.Fatalf("body=%s", body)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 attempts, got %d", n)
	}
}

// The guard the fix must not break: a caller that cancels, or whose own
// deadline expires, gets no replay of the call it just stopped.
func TestRetryRoundTripper_StillRefusesToReplayCallerDeadline(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()

	rt := newRetryRoundTripper(http.DefaultTransport.(*http.Transport).Clone(), retryOptions{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond})
	client := &http.Client{Transport: rt}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected the caller deadline to fail the exchange")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a caller deadline must not be replayed: got %d attempts", n)
	}
}

// peekBody reads a rejection body inside RoundTrip, before any retry decision,
// so an unbounded peek stalls the retry layer on exactly the overloaded
// providers whose 429/503 responses reach the classifier. It must stay bounded
// AND hand back a body that still contains every unread byte.
func TestPeekBodyIsBoundedAndPreservesTheRest(t *testing.T) {
	withWatchdogTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)

	body := strings.Repeat("x", maxErrorPeekBytes) + "TAIL-AFTER-PEEK"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}

	head := peekBody(resp)
	if len(head) != maxErrorPeekBytes {
		t.Fatalf("peeked %d bytes, want %d", len(head), maxErrorPeekBytes)
	}
	// The classifier's peek must not consume the stream the caller still reads.
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if string(rest) != body {
		t.Fatalf("re-fronted body lost bytes: got %d, want %d", len(rest), len(body))
	}
}

// A rejection body that stalls must not hold the peek open.
func TestPeekBodyStallIsBounded(t *testing.T) {
	withWatchdogTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)
	release := make(chan struct{})
	defer close(release)

	resp := &http.Response{Body: io.NopCloser(readerThatBlocks(release))}
	done := make(chan struct{})
	go func() { defer close(done); _ = peekBody(resp) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("peekBody blocked on a stalled rejection body")
	}
}

// readerThatBlocks returns a reader whose first Read blocks until release.
func readerThatBlocks(release <-chan struct{}) io.Reader {
	return blockingUntilReader{release: release}
}

type blockingUntilReader struct{ release <-chan struct{} }

func (r blockingUntilReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}
