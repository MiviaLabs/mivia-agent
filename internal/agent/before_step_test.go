package agent

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type beforeStepCompleter struct{}

func (beforeStepCompleter) Name() string { return "before-step" }
func (beforeStepCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return "done", nil
}
func (beforeStepCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return "done", nil
}
func (beforeStepCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

func TestBeforeStepInjectsBeforePrune(t *testing.T) {
	reg := tools.NewRegistry()
	loop := &Loop{Completer: beforeStepCompleter{}, Tools: reg}
	injected := false
	opts := Options{
		Model:    "m",
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
}
