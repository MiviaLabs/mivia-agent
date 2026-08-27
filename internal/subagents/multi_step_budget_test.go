package subagents

// Total-budget tests for the multi-step handler. The incident: a provider
// connection trickled bytes for over ten minutes after the subagent's final
// report was visible, so idle watchdogs never fired and no total wall clock
// existed to end the run. These tests pin that TotalTimeout really fires
// against a hung provider and that the deferred done event stamps the run
// "timed_out" the same way a per-task deadline does.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// hungUntilDeadlineCompleter blocks every call until the call context's
// deadline fires, then returns the context error - the wire-level shape of
// a trickling or stalled provider connection.
type hungUntilDeadlineCompleter struct {
	name string
}

func (c *hungUntilDeadlineCompleter) Name() string { return c.name }

func (c *hungUntilDeadlineCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (c *hungUntilDeadlineCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

func (c *hungUntilDeadlineCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if _, err := c.Chat(ctx, req); err != nil {
		return nil, err
	}
	return nil, context.Canceled
}

// TestMultiStepTotalTimeoutFiresAndStampsTimedOut drives the incident gap:
// no parent deadline, no request timeout, only the handler's TotalTimeout.
// The hung completer must be cut off by the budget (the error wraps
// context.DeadlineExceeded), and the run's deferred done event must stamp
// Status "timed_out" so the pool, ledger, and UI row all settle.
func TestMultiStepTotalTimeoutFiresAndStampsTimedOut(t *testing.T) {
	events := make(chan agent.Event, 16)
	h := &MultiStepHandler{
		Completer:    &hungUntilDeadlineCompleter{name: "hung"},
		FullRegistry: newTestRegistry(),
		Model:        "test-model",
		SystemPrompt: "Test sub-agent.",
		MaxSteps:     3,
		MaxTokens:    1024,
		TotalTimeout: 50 * time.Millisecond,
		OnEvent:      func(e agent.Event) { events <- e },
	}
	start := time.Now()
	_, err := h.Invoke(context.Background(), runtime.Request{
		Name:  "multi_step",
		Input: json.RawMessage(`"analyze the module"`),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded from the total budget", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("run took %v; the total budget did not fire", elapsed)
	}
	close(events)
	var done *agent.Event
	for e := range events {
		if e.Kind == agent.EventSubagentDone {
			done = &e
		}
	}
	if done == nil {
		t.Fatal("no EventSubagentDone emitted; a run that ends must say how it ended")
	}
	if done.Status != "timed_out" {
		t.Fatalf("done status = %q, want timed_out", done.Status)
	}
}
