package agent

import (
	"context"
	"io"
	"sync"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// docs/product/config.md says max_steps "bounds one turn's agent loop".
// The stop-time continuations (the empty-response retry, the unacted
// continuation) run as iterations of the turn's own SDK loop through the
// ContinueOnStop hook, so the loop's MaxIterations is the per-turn total by
// itself. These tests pin that contract end to end through Loop.Run: a
// turn - however many continuations fire inside it - may never exceed the
// configured step bound, and max_steps = 0 must stay unbounded rather than
// become a computed number that ends a turn early.

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

// TestBudgetAgreementGuardObservesShrunkenBound pins the positional
// agreement between shrinkStepBudgetForReRun (loop_dispatch.go) and the
// ContinueOnStop guard (continue_on_stop.go:81-83): the guard reads the
// shrunken bound, not the original MaxSteps.
func TestBudgetAgreementGuardObservesShrunkenBound(t *testing.T) {
	opts := Options{Model: "m", MaxSteps: 5}
	if !shrinkStepBudgetForReRun(&opts, 5, 2) {
		t.Fatal("shrink with remaining budget must proceed")
	}
	if opts.MaxSteps != 3 {
		t.Fatalf("shrunken MaxSteps = %d, want 3", opts.MaxSteps)
	}
	turn := newSDKTurnState()
	turn.bumpIteration()
	turn.bumpIteration()
	turn.bumpIteration()
	sdkOpts := sdkagentloop.Options{Bounds: sdkagentloop.Bounds{MaxIterations: 3}}
	// RequireFinalText must be true: with false the StopEmptyResponse leg
	// returns nil at continue_on_stop.go:92 regardless of the budget guard,
	// and the test would pass with the guard deleted.
	hook := newSDKContinueOnStop(&Loop{}, sdkOpts, Options{Model: "m", RequireFinalText: true}, turn, "u")
	// Mutating the original after construction must not affect the guard:
	// installSDKContinueOnStop passes *out by value (agentloop_adapter.go:183).
	sdkOpts.Bounds.MaxIterations = 99
	d := sdkagentloop.StopDecision{Stop: sdkagentloop.StopEmptyResponse}
	if got := hook(context.Background(), d); got != nil {
		t.Fatalf("exhausted budget must return nil, got %v", got)
	}
}

// TestShrinkStepBudgetExhaustedRefusesReRun pins the exhausted branch:
// remaining <= 0 refuses instead of mapping to 0 (which the SDK reads
// as uncapped).
func TestShrinkStepBudgetExhaustedRefusesReRun(t *testing.T) {
	opts := Options{Model: "m", MaxSteps: 5}
	if shrinkStepBudgetForReRun(&opts, 5, 5) {
		t.Fatal("exhausted budget must refuse the re-run")
	}
	if opts.MaxSteps != 5 {
		t.Fatalf("refused shrink must leave MaxSteps = %d, want 5", opts.MaxSteps)
	}
}
