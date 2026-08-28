package provider

import (
	"context"
	"strings"
	"testing"
)

// errWriter fails on the first write, standing in for a consumer-side pipe
// that closed mid-stream.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// TestReadStreamContextCanceledReturnsCtxErr: a canceled context (not a
// deadline) must surface context.Canceled with whatever content already
// arrived, unwrapped - the caller distinguishes user aborts from timeouts.
func TestReadStreamContextCanceledReturnsCtxErr(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "http://127.0.0.1:1", APIKey: "k"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")

	out, err := c.readStream(ctx, Request{Model: "m"}, body, nil)
	if out != "" {
		t.Fatalf("out = %q, want empty: the canceled context is checked before any line is scanned", out)
	}
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestReadStreamWriterErrorSurfaces: when the downstream writer fails, the
// error propagates immediately and the content written so far is returned -
// the turn is not silently swallowed as an empty stream.
func TestReadStreamWriterErrorSurfaces(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "http://127.0.0.1:1", APIKey: "k"})
	body := strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	wErr := errWriter{err: context.Canceled}

	out, err := c.readStream(context.Background(), Request{Model: "m"}, body, wErr)
	if out != "hello" {
		t.Fatalf("out = %q, want the accumulated content", out)
	}
	if err != context.Canceled {
		t.Fatalf("err = %v, want the writer error", err)
	}
}

// TestReadStreamEmptyDisableReplayErrors: a stream that ends without a single
// answer and with replay disabled must fail with the no-response error
// instead of attempting the non-stream path.
func TestReadStreamEmptyDisableReplayErrors(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "http://127.0.0.1:1", APIKey: "k"})
	body := strings.NewReader(": keepalive only, no data lines\n\n")

	out, err := c.readStream(context.Background(), Request{Model: "m", DisableProviderReplay: true}, body, nil)
	if out != "" {
		t.Fatalf("out = %q, want empty", out)
	}
	if err == nil || !strings.Contains(err.Error(), "stream delivered no response") {
		t.Fatalf("err = %v, want the no-response error", err)
	}
}
