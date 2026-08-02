package provider

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestReadTurnStreamSurfacedDeadlineError covers readTurnStream's
// scanner-error deadline branch: a DeadlineExceeded surfaced by the underlying
// reader (rather than a clean EOF from the transport closing a stalled
// connection) must produce an error naming the armed request deadline while
// staying errors.Is(context.DeadlineExceeded).
func TestReadTurnStreamSurfacedDeadlineError(t *testing.T) {
	c := &OpenAICompat{name: "test", errorParser: openaiErrorParser}
	_, _, _, _, _, _, _, err := c.readTurnStream(
		context.Background(),
		deadlineReadErrorReader{},
		io.Discard,
		50*time.Millisecond,
	)
	if err == nil {
		t.Fatal("expected a deadline error from the failing stream reader")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err=%v", err)
	}
	if !strings.Contains(err.Error(), "request deadline") || !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("err=%q should name the armed 50ms request deadline", err)
	}
}
