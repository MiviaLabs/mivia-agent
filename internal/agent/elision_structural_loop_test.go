package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestLoopRealPrepKeepsElisionAcrossNonCompactingStep uses a real
// StructuralPreparationManager so the first step elides a prior oversized
// tool body and a later non-compacting step keeps Compacted + counters.
func TestLoopRealPrepKeepsElisionAcrossNonCompactingStep(t *testing.T) {
	marker := "ELISION_MARK_" + strings.Repeat("Z", 3000)
	history := priorElisionHistory(marker)
	reg := elisionToolRegistry()
	principal, binding := elisionPrincipalBinding(t)
	nextUser := "current objective"
	cost := forceElisionBudget(t, history, nextUser, reg)

	prepCalls := 0
	counting := &countingStructuralPrep{inner: contextmgr.StructuralPreparationManager{}, calls: &prepCalls}
	loop := &Loop{Completer: &twoStepCompleter{}, Tools: reg, Messages: history}
	_, err := loop.Run(context.Background(), nextUser, Options{
		Model: "model", MaxContextTokens: cost, MaxSteps: 5,
		PreparationManager: counting,
		PreparationInput: contextmgr.PrepareInput{
			Budget: cost, Principal: principal, Binding: binding,
			Revision: contextstate.Revision{Session: 1, Durable: 1, Source: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRealPrepElision(t, loop, prepCalls, marker)
}

func priorElisionHistory(marker string) []provider.Message {
	oldCall := provider.ToolCall{ID: "call-old", Type: "function"}
	oldCall.Function.Name = "elision_probe_tool"
	oldCall.Function.Arguments = `{}`
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "old objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{oldCall}},
		{Role: provider.RoleTool, ToolCallID: oldCall.ID, Name: oldCall.Function.Name, Content: marker},
		{Role: provider.RoleAssistant, Content: "prior done"},
	}
}

func elisionToolRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(&fixedElisionTool{name: "elision_probe_tool", body: "ok"})
	// twoStepCompleter (shared package fixture) calls "echo" on step 1.
	reg.Register(&fixedElisionTool{name: "echo", body: "ok"})
	return reg
}

func elisionPrincipalBinding(t *testing.T) (contextstate.Principal, contextstate.BindingRevision) {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("two-step", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	return principal, binding
}

func forceElisionBudget(t *testing.T, history []provider.Message, nextUser string, reg *tools.Registry) int {
	t.Helper()
	probe := append(append([]provider.Message{}, history...), provider.Message{Role: provider.RoleUser, Content: nextUser})
	cost, err := provider.EstimatePromptCost(probe, reg.OpenAITools(), provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	return cost
}

func assertRealPrepElision(t *testing.T, loop *Loop, prepCalls int, marker string) {
	t.Helper()
	if prepCalls < 2 {
		t.Fatalf("prepare calls=%d, want ≥2 (multi-step)", prepCalls)
	}
	if !loop.HasPreparation || !loop.LastPreparation.Compacted {
		t.Fatal("final preparation lost Compacted after non-compacting step")
	}
	if loop.LastPreparation.ElidedMessages < 1 {
		t.Fatalf("ElidedMessages=%d, want ≥1", loop.LastPreparation.ElidedMessages)
	}
	if loop.LastPreparation.ElidedBytes != len(marker) {
		t.Fatalf("ElidedBytes=%d, want %d", loop.LastPreparation.ElidedBytes, len(marker))
	}
	found := false
	for _, msg := range loop.Messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "call-old" {
			found = true
			if msg.Content == marker || !strings.HasPrefix(msg.Content, "[context elided prior tool result;") {
				t.Fatalf("prior tool body not elided: %q", trunc(msg.Content, 80))
			}
		}
		if msg.Content == marker {
			t.Fatal("original marker still present in live history")
		}
	}
	if !found {
		t.Fatal("elided prior tool message missing from live history")
	}
	if err := provider.ValidateToolPairing(loop.Messages); err != nil {
		t.Fatalf("pairing: %v", err)
	}
}

type fixedElisionTool struct {
	name string
	body string
}

func (t *fixedElisionTool) Name() string               { return t.name }
func (t *fixedElisionTool) Description() string        { return "fixed body" }
func (t *fixedElisionTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *fixedElisionTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}
func (t *fixedElisionTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.body, nil
}

// countingStructuralPrep wraps StructuralPreparationManager to count Prepare calls.
type countingStructuralPrep struct {
	inner contextmgr.StructuralPreparationManager
	calls *int
}

func (c *countingStructuralPrep) Prepare(ctx context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	*c.calls++
	return c.inner.Prepare(ctx, input)
}
func (c *countingStructuralPrep) Discard(p contextmgr.Preparation) { c.inner.Discard(p) }

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Ensure twoStepCompleter is available from elision_accumulation_test.go in the
// same package. It returns a tool call on step 1 and a final answer on step 2.
var _ interface {
	Name() string
	Chat(context.Context, provider.Request) (string, error)
	ChatStream(context.Context, provider.Request, io.Writer) (string, error)
	ChatTurn(context.Context, provider.Request) (*provider.Response, error)
} = (*twoStepCompleter)(nil)
