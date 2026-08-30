package agent

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// This file drives the continuation through the whole loop, not just the
// predicate: the reported failure is that a turn ENDS after announcing work,
// so the property that matters is that the same turn goes on to do it.

// narrateThenActCompleter reproduces the reported behaviour: the first turn
// announces work and calls nothing; once continued, it does the work. It
// records every request so a test can assert what the continuation sent.
type narrateThenActCompleter struct {
	mu       sync.Mutex
	calls    int
	requests []provider.Request
}

func (c *narrateThenActCompleter) Name() string { return "narrate-then-act" }

func (c *narrateThenActCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}

func (c *narrateThenActCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}

func (c *narrateThenActCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	switch call {
	case 1:
		return &provider.Response{Content: "I'll read the config file now.", FinishReason: "stop"}, nil
	case 2:
		return &provider.Response{FinishReason: "tool_calls", ToolCalls: []provider.ToolCall{tc("1", "read_file", `{}`)}}, nil
	default:
		return &provider.Response{Content: "done", FinishReason: "stop"}, nil
	}
}

func (c *narrateThenActCompleter) recorded() (int, []provider.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, append([]provider.Request(nil), c.requests...)
}

func readFileRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "read_file", class: tools.ExecutionRead, key: "path:a"})
	return reg
}

// TestUnactedTurnContinuesIntoRealWork is the end-to-end regression: a turn
// that announced work and called nothing must, with the operator's opt-in,
// go on to call the tool inside the SAME turn instead of handing control
// back to the user.
func TestUnactedTurnContinuesIntoRealWork(t *testing.T) {
	reg := readFileRegistry()
	comp := &narrateThenActCompleter{}
	loop := &Loop{Completer: comp, Tools: reg}
	out, err := loop.Run(context.Background(), "read the config", Options{
		Model:                   "m",
		MaxSteps:                5,
		AdvertisedToolSpecs:     reg.OpenAITools(),
		MaxUnactedContinuations: 1,
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	calls, requests := comp.recorded()
	// Exactly three: the announcement, the continued step that calls the
	// tool, and its follow-up. A looser bound would let a leak in the
	// continuation budget pass unnoticed.
	if calls != 3 {
		t.Fatalf("want the announcement, the continued step, and its follow-up: got %d provider calls", calls)
	}
	if out != "done" {
		t.Fatalf("final text = %q, want the continued turn's answer", out)
	}
	// The continuation must carry the model's own message plus the notice,
	// so it resumes its plan rather than restarting from the user prompt.
	second := requests[1].Messages
	if len(second) < 2 {
		t.Fatalf("continued request has %d messages", len(second))
	}
	if got := second[len(second)-1]; got.Role != provider.RoleUser || got.Content != unactedContinuationNotice {
		t.Fatalf("continued request must end with the notice, got %+v", got)
	}
	if second[len(second)-2].Content != "I'll read the config file now." {
		t.Fatalf("the model's own announcement must survive into the continuation: %+v", second[len(second)-2])
	}
	// The notice is written back into session history and replays on every
	// later turn, so its persistence is a contract, not an accident: it
	// must be there, and it must be labelled so a later turn cannot read
	// host prose as the user's own words.
	var persisted *provider.Message
	for i := range loop.Messages {
		if loop.Messages[i].Content == unactedContinuationNotice {
			persisted = &loop.Messages[i]
		}
	}
	if persisted == nil {
		t.Fatal("the continuation notice must persist in session history")
	}
	if persisted.Role != provider.RoleUser {
		t.Fatalf("the notice must persist as a user turn (RoleSystem is only valid at index 0), got %q", persisted.Role)
	}
	if !strings.HasPrefix(persisted.Content, "[mivia:") {
		t.Fatalf("the persisted notice must be labelled as host prose: %q", persisted.Content)
	}
}

// TestUnactedTurnNotContinuedWhenDisabled pins the default: the same
// completer, with no opt-in, ends the turn after the announcement and makes
// exactly one provider call.
func TestUnactedTurnNotContinuedWhenDisabled(t *testing.T) {
	reg := readFileRegistry()
	comp := &narrateThenActCompleter{}
	loop := &Loop{Completer: comp, Tools: reg}
	out, err := loop.Run(context.Background(), "read the config", Options{
		Model:               "m",
		MaxSteps:            5,
		AdvertisedToolSpecs: reg.OpenAITools(),
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if calls, _ := comp.recorded(); calls != 1 {
		t.Fatalf("want exactly 1 provider call with the mechanism off, got %d", calls)
	}
	if out != "I'll read the config file now." {
		t.Fatalf("final text = %q, want the announcement itself", out)
	}
}

// TestUnactedTurnContinuationIsBounded pins that a model which keeps
// announcing without acting is continued a bounded number of times, never
// in a loop.
func TestUnactedTurnContinuationIsBounded(t *testing.T) {
	reg := readFileRegistry()
	comp := &alwaysNarratingCompleter{}
	loop := &Loop{Completer: comp, Tools: reg}
	if _, err := loop.Run(context.Background(), "read the config", Options{
		Model:                   "m",
		MaxSteps:                5,
		AdvertisedToolSpecs:     reg.OpenAITools(),
		MaxUnactedContinuations: 2,
	}); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if got := comp.count(); got != 3 {
		t.Fatalf("want 1 original call + 2 bounded continuations, got %d", got)
	}
}

// alwaysNarratingCompleter never acts, whatever it is told.
type alwaysNarratingCompleter struct {
	mu    sync.Mutex
	calls int
}

func (c *alwaysNarratingCompleter) Name() string { return "always-narrating" }

func (c *alwaysNarratingCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}

func (c *alwaysNarratingCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}

func (c *alwaysNarratingCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return &provider.Response{Content: "I'll read the config file now.", FinishReason: "stop"}, nil
}

func (c *alwaysNarratingCompleter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}
