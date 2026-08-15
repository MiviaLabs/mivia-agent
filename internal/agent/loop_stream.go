package agent

import (
	"context"
	"io"
	"strings"
	"time"
)

// teeWriter mirrors streamed assistant deltas to both the caller's writer and
// an EventAssistant stream, and accumulates the full text for callers that
// need it after the stream ends.
type teeWriter struct {
	w    io.Writer
	buf  strings.Builder
	opts Options
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if len(p) > 0 {
		emit(t.opts, Event{Kind: EventAssistant, Content: string(p), Detail: "delta"})
	}
	if t.w == nil {
		return len(p), nil
	}
	return t.w.Write(p)
}

func (t *teeWriter) String() string { return t.buf.String() }

// streamRevoker is implemented by the TUI streamBridge to clear optimistic
// content that was streamed before tool_calls arrived.
type streamRevoker interface {
	RevokeStream() string
}

func revokeStreamWriter(w io.Writer) {
	if w == nil {
		return
	}
	if r, ok := w.(streamRevoker); ok {
		_ = r.RevokeStream()
	}
}

// modelThinkingHeartbeatInterval is the UI progress cadence while a provider
// request is in flight. Overridable in tests.
var modelThinkingHeartbeatInterval = 2 * time.Second

// emitModelThinkingHeartbeat runs the model-thinking progress heartbeat at the
// current package-level cadence. It exists for tests that override the interval
// before calling; production uses emitModelThinkingHeartbeatAt so the read
// happens before the goroutine spawns.
func emitModelThinkingHeartbeat(ctx context.Context, opts Options) {
	emitModelThinkingHeartbeatAt(ctx, opts, modelThinkingHeartbeatInterval)
}

// emitModelThinkingHeartbeatAt is the heartbeat loop. interval is captured by
// the caller so the package-level override variable is never read inside the
// goroutine (data-race-free under -race with concurrent test overrides).
func emitModelThinkingHeartbeatAt(ctx context.Context, opts Options, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			emit(opts, Event{
				Kind:   EventHeartbeat,
				Detail: "working",
			})
		case <-ctx.Done():
			return
		}
	}
}
