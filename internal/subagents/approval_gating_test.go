package subagents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// A subagent's nested loop built its agent.Options with NO approval fields at
// all - no gate, no policy, no standing cache. So a write tool the operator
// would be asked about on the root path ran unprompted the moment the model
// delegated it. An operator who set "deny" was enforced against directly and
// bypassed by delegation.

// gatedWriteTool is Write-class, so any policy but auto must decide about it.
type gatedWriteTool struct{ ran bool }

func (*gatedWriteTool) Name() string               { return "write_file" }
func (*gatedWriteTool) Description() string        { return "writes" }
func (*gatedWriteTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*gatedWriteTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "write_file"}
}
func (t *gatedWriteTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.ran = true
	return "WROTE THE FILE", nil
}

// writeCall is the model's one tool call: the write tool, with no arguments.
func writeCall() provider.ToolCall {
	var call provider.ToolCall
	call.ID, call.Type = "call-1", "function"
	call.Function.Name, call.Function.Arguments = "write_file", `{}`
	return call
}

// runSubagentWith drives a real subagent turn whose one tool call is the write
// tool, under the supplied approval wiring.
func runSubagentWith(t *testing.T, approval func() sdkadapter.ApprovalDeps) *gatedWriteTool {
	t.Helper()

	tool := &gatedWriteTool{}
	reg := tools.NewRegistry()
	reg.Register(tool)

	srv := subagentHTTPServer(t, []struct {
		content   string
		toolCalls []provider.ToolCall
	}{
		{toolCalls: []provider.ToolCall{writeCall()}},
		{content: "done"},
	})
	defer srv.Close()

	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{
		Name: "test-sub", BaseURL: srv.URL, APIKey: "test-key",
	})
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)

	handler := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: reg,
		Dispatcher:   d,
		Model:        "sub-model",
		MaxSteps:     3,
		MaxTokens:    200,
		Approval:     approval,
	}
	if _, err := handler.Invoke(context.Background(), runtime.Request{
		ID: "task-1", Name: "worker", Input: json.RawMessage(`"do the work"`),
	}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	return tool
}

// TestASubagentHonoursADenyPolicy is the reported hole.
func TestASubagentHonoursADenyPolicy(t *testing.T) {
	tool := runSubagentWith(t, func() sdkadapter.ApprovalDeps {
		return sdkadapter.ApprovalDeps{Policy: "deny"}
	})

	if tool.ran {
		t.Fatal("a subagent executed a write tool under a \"deny\" policy; the " +
			"operator is enforced against on the root path and bypassed the moment " +
			"the model delegates the same call")
	}
}

// TestASubagentAsksTheOperatorsGate proves the gate is reached, not merely
// that something refused.
func TestASubagentAsksTheOperatorsGate(t *testing.T) {
	var asked []string
	tool := runSubagentWith(t, func() sdkadapter.ApprovalDeps {
		return sdkadapter.ApprovalDeps{
			Policy: "write-only",
			Gate: func(_ context.Context, name string, _ json.RawMessage) sdkadapter.ApprovalResult {
				asked = append(asked, name)
				return sdkadapter.ApprovalResult{Approved: false, Err: "denied"}
			},
		}
	})

	if len(asked) == 0 {
		t.Fatal("the operator's gate was never asked about a subagent's write tool")
	}
	if asked[0] != "write_file" {
		t.Errorf("the gate was asked about %q, want write_file", asked[0])
	}
	if tool.ran {
		t.Error("the tool ran despite the gate refusing it")
	}
}

// TestAnApprovedSubagentCallStillRuns keeps delegation working: gating must
// not become a refusal of everything.
func TestAnApprovedSubagentCallStillRuns(t *testing.T) {
	tool := runSubagentWith(t, func() sdkadapter.ApprovalDeps {
		return sdkadapter.ApprovalDeps{
			Policy: "write-only",
			Gate: func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
				return sdkadapter.ApprovalResult{Approved: true}
			},
		}
	})

	if !tool.ran {
		t.Error("an approved subagent tool call did not run")
	}
}

// TestASubagentWithNoApprovalWiringStillRuns is the compatibility case. Every
// construction site must pass the accessor; until they all do, a handler
// without one must behave as it does today rather than deadlocking a run.
func TestASubagentWithNoApprovalWiringStillRuns(t *testing.T) {
	tool := runSubagentWith(t, nil)

	if !tool.ran {
		t.Error("a subagent with no approval wiring refused its own tool call; " +
			"an unwired construction site would deadlock instead of degrading")
	}
}
