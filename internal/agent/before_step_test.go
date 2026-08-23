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

type beforeStepCompleter struct {
	mu       sync.Mutex
	requests []provider.Request
}

func (c *beforeStepCompleter) Name() string { return "before-step" }
func (c *beforeStepCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return "done", nil
}
func (c *beforeStepCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return "done", nil
}
func (c *beforeStepCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

func (c *beforeStepCompleter) seen() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.requests...)
}

// TestBeforeStepInjectsBeforePrune runs on the default (SDK) backend:
// the Steer injector delivers the framed parent message at the top of
// the very first iteration, so both the request the provider saw and
// the loop's written-back history carry the frame.
func TestBeforeStepInjectsBeforePrune(t *testing.T) {
	reg := tools.NewRegistry()
	comp := &beforeStepCompleter{}
	loop := &Loop{Completer: comp, Tools: reg}
	injected := false
	opts := Options{Model: "m",
		MaxSteps: 1,
		BeforeStep: func() []provider.Message {
			injected = true
			return []provider.Message{{
				Role:    provider.RoleUser,
				Content: FrameParentMessage("steer body"),
			}}
		},
	}
	text, err := loop.Run(context.Background(), "user task", opts)
	if err != nil {
		t.Fatal(err)
	}
	if text != "done" || !injected {
		t.Fatalf("text=%q injected=%v", text, injected)
	}
	found := false
	for _, m := range loop.Messages {
		if strings.Contains(m.Content, "steer body") && strings.Contains(m.Content, "<parent-message>") {
			found = true
		}
	}
	if !found {
		t.Fatalf("injected frame missing from history: %+v", loop.Messages)
	}
	inRequest := false
	for _, req := range comp.seen() {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "steer body") && strings.Contains(m.Content, "<parent-message>") {
				inRequest = true
			}
		}
	}
	if !inRequest {
		t.Fatal("injected frame never reached a provider request")
	}
}
