package agent

// Shared scripted-completer and harness helpers for the soft-interrupt (steer)
// loop tests (plan 54 §7). Helpers in any _test.go file of package agent are
// visible to the others, so steerStep, steerCompleter, gateTool, runLoop and
// messagesContain live here for both loop_steer_test.go and
// loop_steer_watchdog_test.go.

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// steerStep scripts one completer call.
type steerStep struct {
	resp     provider.Response // response returned when the call completes
	err      error             // if non-nil, returned immediately instead of resp
	blockCtx bool              // block on ctx.Done(), then return ctx.Err()
	gate     chan struct{}     // if non-nil, wait for close (or ctx cancel) before resp
	partial  string            // if non-empty, stream to req.StreamWriter before blocking
	// cancelErr, when non-nil, replaces ctx.Err() as the error a canceled call
	// returns (blockCtx or gate ctx.Done()). Lets a test script a provider that
	// surfaces its OWN failure racing a steer cancel instead of the cancel.
	cancelErr error
	// waitGateOnly blocks on gate WITHOUT observing ctx cancellation: used to
	// script a completer that returns ctx.Err() only after the test has already
	// canceled the turn ctx (deterministic hard-cancel-racing-steer ordering).
	waitGateOnly bool
}

// steerCompleter is a scripted completer for soft-interrupt tests. Every call
// is recorded on the loop goroutine; the first started signal is published on
// started for test-side handshakes.
type steerCompleter struct {
	steps    []steerStep
	started  chan struct{} // buffered; signaled (non-blocking) when a call begins
	requests []provider.Request
	calls    int
	canceled []int // 0-based call indices that observed ctx cancellation
}

func (s *steerCompleter) Name() string { return "steer" }
func (s *steerCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := s.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (s *steerCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return s.Chat(ctx, req)
}
func (s *steerCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	s.requests = append(s.requests, req)
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	idx := s.calls
	s.calls++
	step := steerStep{resp: provider.Response{Content: "done", FinishReason: "stop"}}
	if idx < len(s.steps) {
		step = s.steps[idx]
		// A step that only configures blocking defaults to the same "done"
		// response the completer returns past its script.
		if step.resp.Content == "" && step.resp.FinishReason == "" && len(step.resp.ToolCalls) == 0 {
			step.resp = provider.Response{Content: "done", FinishReason: "stop"}
		}
	}
	if step.partial != "" && req.Stream && req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, step.partial)
	}
	if step.err != nil {
		return nil, step.err
	}
	if step.blockCtx {
		<-ctx.Done()
		s.canceled = append(s.canceled, idx)
		if step.cancelErr != nil {
			return nil, step.cancelErr
		}
		return nil, ctx.Err()
	}
	if step.waitGateOnly {
		<-step.gate
		s.canceled = append(s.canceled, idx)
		return nil, ctx.Err()
	}
	if step.gate != nil {
		select {
		case <-step.gate:
		case <-ctx.Done():
			s.canceled = append(s.canceled, idx)
			if step.cancelErr != nil {
				return nil, step.cancelErr
			}
			return nil, ctx.Err()
		}
	}
	return &step.resp, nil
}

// gateTool blocks in Execute until release is closed (or ctx is canceled),
// signaling started when work actually begins.
type gateTool struct {
	name     string
	release  chan struct{}
	started  chan struct{} // buffered; signaled when Execute begins
	executed atomic.Bool
}

func (t *gateTool) Name() string               { return t.name }
func (t *gateTool) Description() string        { return "gate test tool" }
func (t *gateTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *gateTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "gate"}
}
func (t *gateTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	if t.started != nil {
		select {
		case t.started <- struct{}{}:
		default:
		}
	}
	select {
	case <-t.release:
		t.executed.Store(true)
		return "secret-result", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// runLoop runs l.Run on a fresh goroutine so a broken implementation cannot
// hang the whole test binary; 5s is far beyond any soft-interrupt path.
func runLoop(t *testing.T, l *Loop, ctx context.Context, userText string, opts Options) (string, error) {
	t.Helper()
	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		text, err := l.Run(ctx, userText, opts)
		ch <- result{text: text, err: err}
	}()
	select {
	case r := <-ch:
		return r.text, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("Loop.Run did not return within 5s")
		return "", nil
	}
}

func messagesContain(messages []provider.Message, text string) bool {
	for _, m := range messages {
		if strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}
