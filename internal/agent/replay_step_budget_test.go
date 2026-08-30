package agent

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// docs/product/config.md says max_steps "bounds one turn's agent loop".
// A host-side replay (the empty-response retry, the unacted continuation)
// re-runs the SDK loop, and every re-run used to start with a fresh
// MaxIterations. One turn could then execute max_steps x (1 + replays)
// provider calls while the operator read the documented bound as a
// per-turn total.

// emptyThenNarratingCompleter answers the first call with a genuinely empty
// response (which drives StopEmptyResponse and the empty-response retry),
// then keeps answering with text and no tool calls. Every call is counted,
// so a test can compare the total against the configured step bound.
type emptyThenNarratingCompleter struct {
	mu    sync.Mutex
	calls int
}

func (c *emptyThenNarratingCompleter) Name() string { return "empty-then-narrating" }

func (c *emptyThenNarratingCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}

func (c *emptyThenNarratingCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}

func (c *emptyThenNarratingCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	c.calls++
	first := c.calls == 1
	c.mu.Unlock()
	if first {
		return &provider.Response{FinishReason: "stop"}, nil
	}
	return &provider.Response{Content: "answering now.", FinishReason: "stop"}, nil
}

func (c *emptyThenNarratingCompleter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestEmptyResponseRetryHonorsTheTurnStepBound is the regression: with
// max_steps = 1, one turn must make at most one provider call in total,
// however many replays fire inside it.
func TestEmptyResponseRetryHonorsTheTurnStepBound(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a"})
	comp := &emptyThenNarratingCompleter{}
	loop := &Loop{Completer: comp, Tools: reg}
	_, _ = loop.Run(context.Background(), "do the work", Options{
		Model:               "m",
		MaxSteps:            1,
		RequireFinalText:    true,
		AdvertisedToolSpecs: reg.OpenAITools(),
	})
	if got := comp.count(); got > 1 {
		t.Fatalf("max_steps = 1 bounds the whole turn: got %d provider calls", got)
	}
}

// TestUnactedContinuationHonorsTheTurnStepBound pins the same rule for the
// other replay: two steps of budget must cover the original run AND its
// continuation, not two steps each.
func TestUnactedContinuationHonorsTheTurnStepBound(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a"})
	comp := &alwaysNarratingCompleter{}
	loop := &Loop{Completer: comp, Tools: reg}
	_, _ = loop.Run(context.Background(), "do the work", Options{
		Model:                   "m",
		MaxSteps:                2,
		AdvertisedToolSpecs:     reg.OpenAITools(),
		MaxUnactedContinuations: 3,
	})
	if got := comp.count(); got > 2 {
		t.Fatalf("max_steps = 2 bounds the whole turn: got %d provider calls", got)
	}
}

// TestUnboundedStepsStayUnbounded pins the zero-means-unlimited contract
// through the replay path: max_steps = 0 must not become a computed number
// that ends a turn early. The completer narrates forever, so the run is
// bounded only by MaxUnactedContinuations, and every continuation must fire.
func TestUnboundedStepsStayUnbounded(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a"})
	comp := &alwaysNarratingCompleter{}
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "do the work", Options{
		Model:                   "m",
		MaxSteps:                0,
		AdvertisedToolSpecs:     reg.OpenAITools(),
		MaxUnactedContinuations: 2,
	}); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if got := comp.count(); got != 3 {
		t.Fatalf("unbounded steps must still run 1 call + 2 continuations, got %d", got)
	}
}

// TestRemainingStepBudget states the arithmetic directly, including the
// two cases the callers depend on: unbounded stays unbounded, and an
// exhausted budget reports that no replay may run.
func TestRemainingStepBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bound     int
		spent     int
		want      int
		exhausted bool
	}{
		{"unbounded stays unbounded", 0, 5, 0, false},
		{"negative bound stays unbounded", -1, 5, -1, false},
		{"budget left", 5, 2, 3, false},
		{"exactly spent", 5, 5, 0, true},
		{"overspent", 5, 7, 0, true},
		{"nothing spent", 5, 0, 5, false},
	} {
		got, exhausted := remainingStepBudget(tc.bound, tc.spent)
		if got != tc.want || exhausted != tc.exhausted {
			t.Errorf("%s: remainingStepBudget(%d, %d) = (%d, %v), want (%d, %v)",
				tc.name, tc.bound, tc.spent, got, exhausted, tc.want, tc.exhausted)
		}
	}
}
