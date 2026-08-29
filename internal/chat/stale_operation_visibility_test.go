package chat

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// supersedingCompleter answers a turn's request and, before returning, runs
// an optional hook - used below to begin a second agent turn WHILE the first
// turn's provider call is still "in flight" from the fencing system's point
// of view, so the first turn's commit races a genuinely newer one exactly
// the way a real superseded turn does.
type supersedingCompleter struct {
	out  string
	hook func()
}

func (c *supersedingCompleter) Name() string { return "superseding" }

func (c *supersedingCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return c.ChatStream(ctx, req, io.Discard)
}

func (c *supersedingCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	if c.hook != nil {
		c.hook()
	}
	if w != nil {
		_, _ = io.WriteString(w, c.out)
	}
	return c.out, nil
}

func (c *supersedingCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if c.hook != nil {
		c.hook()
	}
	return &provider.Response{Content: c.out, FinishReason: "stop"}, nil
}

// captureStderr temporarily redirects os.Stderr to a pipe for the duration
// of fn and returns everything written to it. Not safe to run in parallel
// with other tests that also swap os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	os.Stderr = original
	_ = w.Close()
	var buf strings.Builder
	_, _ = io.Copy(&buf, r)
	_ = r.Close()
	return buf.String()
}

// TestSupersededAgentTurnCommitIsLoggedNotSilent locks the fix for the
// reported data-loss bug: sendAgent (session.go) used to swallow
// ErrStaleOperation from finishAgentTurn with no trace anywhere - a
// superseded turn's history disappeared from memory and was never
// persisted, and there was zero evidence it had happened. A genuinely
// superseded turn (a real newer turn began while this one's provider call
// was in flight) must still be dropped - that part of the design is
// correct and unchanged - but the drop must now be observable, so an
// operator investigating a "history not saving" report has something to go
// on, and a MISCOMPUTED fence (the one thing this test cannot force without
// live reproduction) would leave the same trace instead of none at all.
func TestSupersededAgentTurnCommitIsLoggedNotSilent(t *testing.T) {
	comp := &supersedingCompleter{out: "first reply"}
	sess := agentTurnSession(t, comp)
	comp.hook = func() {
		// A second, genuinely newer turn begins while "first"'s provider
		// call is still outstanding - this is what makes "first"'s eventual
		// commit legitimately stale, not a test artifact.
		_, done, err := sess.beginAgentTurn("second", nil)
		if err != nil {
			t.Fatalf("begin superseding turn: %v", err)
		}
		done()
	}

	var reply string
	var sendErr error
	stderr := captureStderr(t, func() {
		reply, sendErr = sess.SendUser(context.Background(), "first", io.Discard)
	})

	// ErrStaleOperation is still swallowed as a *return value*: the design
	// intent (a superseded turn losing the race is not the caller's error)
	// is unchanged by this fix.
	if sendErr != nil {
		t.Fatalf("SendUser returned an error for a superseded (but otherwise successful) turn: %v", sendErr)
	}
	if reply != "first reply" {
		t.Fatalf("reply = %q, want the completer's own output", reply)
	}
	if blob := historyBlob(sess); strings.Contains(blob, "first reply") {
		t.Fatalf("superseded turn's history was adopted into the session: %s", blob)
	}

	// This is the actual fix: the swallow is no longer silent.
	if !strings.Contains(stderr, "not persisted") {
		t.Fatalf("expected the swallowed stale commit to be logged to stderr, got: %q", stderr)
	}
}

// TestSuccessfulAgentTurnLogsNothing pins that the new logging is
// conditional on the stale-operation branch actually firing: an ordinary,
// uncontested turn must not print anything, or every normal turn would
// spam stderr.
func TestSuccessfulAgentTurnLogsNothing(t *testing.T) {
	comp := &supersedingCompleter{out: "answer"}
	sess := agentTurnSession(t, comp)

	var sendErr error
	stderr := captureStderr(t, func() {
		_, sendErr = sess.SendUser(context.Background(), "hello", io.Discard)
	})
	if sendErr != nil {
		t.Fatalf("SendUser: %v", sendErr)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr output for an uncontested turn, got: %q", stderr)
	}
}
