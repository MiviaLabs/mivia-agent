package provider

import (
	"context"
	"errors"
	"io"
	"testing"
)

// cancelOnFirstLineReader delivers one complete SSE line, cancels the
// request context as part of that same Read call (so the cancellation is
// guaranteed to have taken effect before readTurnStream's caller can
// process the delivered chunk and loop back to check ctx.Done()), then
// reports EOF on any further read.
type cancelOnFirstLineReader struct {
	line   []byte
	served bool
	cancel context.CancelFunc
}

func (r *cancelOnFirstLineReader) Read(p []byte) (int, error) {
	if !r.served {
		r.served = true
		n := copy(p, r.line)
		r.cancel()
		return n, nil
	}
	return 0, io.EOF
}

// TestReadTurnStreamDiscardsReasoningOnInterrupt pins the interrupt/cancel
// path an adopting provider (DeepSeek) depends on: readTurnStream must never
// hand back a partial Response carrying reasoning_content (or content) it
// accumulated before the context was canceled mid-stream. The agent loop
// only turns a *Response into a persisted Message on a nil error, so a
// non-nil error here is what keeps an interrupted, reasoning-less partial
// turn out of history in the first place - the caller for chatTurnStream
// (openai_compat_stream.go) returns immediately on this error and never
// constructs a Response from the partially-filled builders.
func TestReadTurnStreamDiscardsReasoningOnInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelOnFirstLineReader{
		line:   []byte(`data: {"choices":[{"delta":{"reasoning_content":"partial-thinking","content":"partial-answer"}}]}` + "\n"),
		cancel: cancel,
	}
	c := &OpenAICompat{name: "test", errorParser: openaiErrorParser}
	content, reasoning, _, toolCalls, finishReason, received, usage, err := c.readTurnStream(ctx, reader, io.Discard, 0)
	if err == nil {
		t.Fatal("expected an error from a context canceled mid-stream")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = false; err=%v", err)
	}
	if content != "" || reasoning != "" || received || len(toolCalls) != 0 || finishReason != "" || usage != nil {
		t.Fatalf("interrupted read must discard accumulated state, got content=%q reasoning=%q received=%v toolCalls=%v finishReason=%q usage=%v",
			content, reasoning, received, toolCalls, finishReason, usage)
	}
}
