package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRetryOnStreamStallCanceledContext: the first attempt runs
// unconditionally, but once the caller's context is dead the retry loop must
// stop dialing and surface ctx.Err() instead of the stall.
func TestRetryOnStreamStallCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := retryOnStreamStall(ctx, func() (int, error) {
		calls++
		return 0, fmt.Errorf("test: %w", ErrStreamIdle)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("made %d calls, want 1 (the unconditional first attempt)", calls)
	}
}

// TestStreamHostileStatusClassTable pins the rejection class: only the four
// stream-shape statuses count. 429/408 are transient-transport business, and
// a 5xx says nothing about whether the provider can stream.
func TestStreamHostileStatusClassTable(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusNotFound, true},
		{http.StatusUnsupportedMediaType, true},
		{http.StatusUnprocessableEntity, true},
		{http.StatusOK, false},
		{http.StatusUnauthorized, false},
		{http.StatusTooManyRequests, false},
		{http.StatusRequestTimeout, false},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
	}
	for _, tc := range cases {
		if got := streamHostileStatus(tc.code); got != tc.want {
			t.Errorf("streamHostileStatus(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

// TestStreamHostileBodyRequiresJSON: a plain-text rejection is not proof the
// provider cannot stream, so it must not set the hostile memory.
func TestStreamHostileBodyRequiresJSON(t *testing.T) {
	jsonResp := &http.Response{Header: http.Header{}}
	jsonResp.Header.Set("Content-Type", "application/json; charset=utf-8")
	if !streamHostileBody(jsonResp) {
		t.Fatal("an application/json rejection was not classified stream-hostile")
	}
	textResp := &http.Response{Header: http.Header{}}
	textResp.Header.Set("Content-Type", "text/plain")
	if streamHostileBody(textResp) {
		t.Fatal("a text/plain rejection was classified stream-hostile")
	}
}

// TestStreamTransportAttemptNewRequestError: a request the client cannot even
// shape (no model) fails before any dial, with no response and no hostile
// verdict - it is this caller's bug, not the provider's stream shape.
func TestStreamTransportAttemptNewRequestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	c := streamTransportClient(t, srv, false)
	resp, err, rejected := c.streamTransportAttempt(context.Background(), Request{})
	if resp != nil || rejected {
		t.Fatalf("resp=%v rejected=%v, want nil/false", resp, rejected)
	}
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("err = %v, want the model-is-required shape error", err)
	}
}

// TestStreamTransportAttemptDialError: a connection the transport cannot
// establish surfaces as a wrapped request failure, still not a hostile
// verdict, so the shared transient machinery stays in charge.
func TestStreamTransportAttemptDialError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	base := srv.URL
	srv.Close() // a closed listener guarantees a dial failure
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: base, APIKey: "k"})
	resp, err, rejected := c.streamTransportAttempt(context.Background(), Request{Model: "m"})
	if resp != nil || rejected {
		t.Fatalf("resp=%v rejected=%v, want nil/false", resp, rejected)
	}
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("err = %v, want a wrapped request-failed error", err)
	}
}

// TestSseTurnAttemptOnceMaxTokensPreBodyFallsBack: a max-tokens-cap rejection
// ahead of the body is a request-shape problem. It must ask for the non-stream
// fallback WITHOUT marking the provider stream-hostile.
func TestSseTurnAttemptOnceMaxTokensPreBodyFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"cap"}}`))
	}))
	defer srv.Close()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: srv.URL, APIKey: "k",
		// A 500 is not in the stream-hostile class, and NonRetryable keeps
		// the shared transport from burning its budget on this test server.
		NonRetryable: func(int, []byte) bool { return true },
		ErrorParser: func(int, []byte) error {
			return fmt.Errorf("test: cap: %w", ErrMaxTokensExceeded)
		},
	})
	req := Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	streamReq := req
	streamReq.Stream = true
	streamReq.StreamWriter = nil

	attempt, err := c.sseTurnAttemptOnce(context.Background(), req, streamReq)
	if err != nil {
		t.Fatalf("err = %v, want nil with needFallback", err)
	}
	if !attempt.needFallback {
		t.Fatal("a max-tokens rejection did not ask for the non-stream fallback")
	}
	if attempt.resp != nil {
		t.Fatalf("resp = %v, want nil", attempt.resp)
	}
	if c.streamHostile.Load() {
		t.Fatal("a max-tokens rejection marked the provider stream-hostile")
	}
}

// TestSseTurnAttemptOnceOtherPreBodyErrorSurfaces: a non-rejected, non-cap
// pre-body failure (5xx dial succeeded) must propagate as-is with no fallback
// request and no hostile memory.
func TestSseTurnAttemptOnceOtherPreBodyErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: srv.URL, APIKey: "k",
		NonRetryable: func(int, []byte) bool { return true },
	})
	req := Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	streamReq := req
	streamReq.Stream = true
	streamReq.StreamWriter = nil

	attempt, err := c.sseTurnAttemptOnce(context.Background(), req, streamReq)
	if err == nil {
		t.Fatal("a 500 pre-body failure must surface an error")
	}
	if attempt.needFallback || attempt.resp != nil {
		t.Fatalf("attempt = %+v, want an empty attempt that propagates the error", attempt)
	}
	if c.streamHostile.Load() {
		t.Fatal("a 500 marked the provider stream-hostile")
	}
}

// TestStreamTransportFallbackDisableReplay: with replay disabled the fallback
// is a hard stop. A stall surfaces as the transient stall cause; a
// completed-but-empty stream surfaces the no-response error.
func TestStreamTransportFallbackDisableReplay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("DisableProviderReplay fallback must not dial the provider")
	}))
	defer srv.Close()
	c := streamTransportClient(t, srv, false)
	req := Request{Model: "m", DisableProviderReplay: true}

	stall := fmt.Errorf("test: %w", ErrStreamIdle)
	_, err := c.streamTransportFallback(context.Background(), req, stall)
	if !errors.Is(err, ErrStreamIdle) || !IsTransient(err) {
		t.Fatalf("stall fallback err = %v, want a transient wrapping ErrStreamIdle", err)
	}

	_, err = c.streamTransportFallback(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "stream delivered no response") {
		t.Fatalf("empty-stream fallback err = %v, want the no-response error", err)
	}
}

// TestStreamTransportFallbackNonStreamFailure: when the terminal non-stream
// attempt also fails with no stall behind it, that failure is the answer - no
// stall wrapper, just the provider error.
func TestStreamTransportFallbackNonStreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: srv.URL, APIKey: "k",
		NonRetryable: func(int, []byte) bool { return true },
	})
	req := Request{Model: "m"}

	_, err := c.streamTransportFallback(context.Background(), req, nil)
	if err == nil {
		t.Fatal("a failed non-stream fallback must surface its error")
	}
	if errors.Is(err, ErrStreamIdle) {
		t.Fatalf("err = %v, want the plain provider failure without a stall wrap", err)
	}
}
