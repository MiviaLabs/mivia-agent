package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type ephemeralCoverageTool struct{}

func (ephemeralCoverageTool) Name() string               { return "ephemeral" }
func (ephemeralCoverageTool) Description() string        { return "" }
func (ephemeralCoverageTool) Parameters() map[string]any { return nil }
func (ephemeralCoverageTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}
func (ephemeralCoverageTool) EphemeralResultMarker(args json.RawMessage) string {
	return "marker:" + string(args)
}

func TestCoverageHelpersContextAndTruncate(t *testing.T) {
	if got := truncate("first\nsecond", 40); got != "first second" {
		t.Fatalf("truncate unchanged length = %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc..." {
		t.Fatalf("truncate shortened = %q, want abc...", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !interruptedContext(ctx, errors.New("other")) {
		t.Fatal("cancelled context was not treated as interrupted")
	}
	if !interruptedContext(context.Background(), context.DeadlineExceeded) {
		t.Fatal("deadline error was not treated as interrupted")
	}
	if interruptedContext(context.Background(), errors.New("other")) {
		t.Fatal("ordinary error was treated as interrupted")
	}

	if err := promptBudgetError(nil, 0, provider.ContextAccountingProfile{}); err != nil {
		t.Fatalf("unlimited budget error = %v", err)
	}
	if err := promptBudgetError([]provider.Message{{Role: provider.RoleUser, Content: "token budget"}}, 1, provider.ContextAccountingProfile{}); !errors.Is(err, ErrPromptBudgetExceeded) {
		t.Fatalf("small budget error = %v, want ErrPromptBudgetExceeded", err)
	}
}

func TestCoverageHelpersScrubEphemeralToolMessages(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(ephemeralCoverageTool{})
	var call provider.ToolCall
	call.ID = "call-1"
	call.Function.Name = "ephemeral"
	call.Function.Arguments = `{"path":"private.txt"}`
	messages := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: "ephemeral", Content: "private body"},
		{Role: provider.RoleTool, ToolCallID: "unknown", Name: "unknown", Content: "keep"},
	}

	ScrubEphemeralToolMessages(messages, registry)
	if got, want := messages[1].Content, `marker:{"path":"private.txt"}`; got != want {
		t.Fatalf("ephemeral tool content = %q, want %q", got, want)
	}
	if got := messages[2].Content; got != "keep" {
		t.Fatalf("unknown tool content = %q, want keep", got)
	}
	ScrubEphemeralToolMessages(messages, nil)
}
