package agent

// Regression tests for the SDK-path staged/unadmitted tool denial:
// the OnToolCallError hook installed in buildAgentLoopOptions must
// mirror the legacy executeToolTask not-in-registry branch
// (loop_tool_exec.go) - StagedToolMessage first, then
// UnadmittedToolHandler, then the generic not-available message -
// each rendered as "error: <msg>" in the RoleTool history entry.

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// toolErrorScript builds a two-step completer script: step 1 requests
// a call to "grep" (a tool NOT in the registry), step 2 answers.
func toolErrorScript() []provider.Response {
	return []provider.Response{
		{ToolCalls: []provider.ToolCall{tc("call1", "grep", `{}`)}, FinishReason: "tool_calls"},
		{Content: "answer", FinishReason: "stop"},
	}
}

// toolResultContent finds the RoleTool history entry for callID and
// returns its Content, or fails the test when absent.
func toolResultContent(t *testing.T, history []sdkshape.Message, callID string) string {
	t.Helper()
	for _, m := range history {
		if m.Role == sdkshape.RoleTool && m.ToolCallID == callID {
			return m.Content
		}
	}
	t.Fatalf("no RoleTool message with ToolCallID %q in history", callID)
	return ""
}

func TestRunAgentLoopOnce_StagedToolCallReturnsPendingNotice(t *testing.T) {
	comp := &scriptCompleter{steps: toolErrorScript()}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:    "m",
		MaxSteps: 3,
		StagedToolMessage: func(name string) (string, bool) {
			if name == "grep" {
				return "tool staged; pending publication", true
			}
			return "", false
		},
	}
	res, err := RunAgentLoopOnce(context.Background(), loop, opts, []provider.Message{{Role: provider.RoleUser, Content: "q"}})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := toolResultContent(t, res.History, "call1"); got != "error: tool staged; pending publication" {
		t.Fatalf("RoleTool Content = %q, want %q (no [tool-error] body)", got, "error: tool staged; pending publication")
	}
}

func TestRunAgentLoopOnce_UnknownToolSaysNotAvailable(t *testing.T) {
	comp := &scriptCompleter{steps: toolErrorScript()}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{Model: "m", MaxSteps: 3}
	res, err := RunAgentLoopOnce(context.Background(), loop, opts, []provider.Message{{Role: provider.RoleUser, Content: "q"}})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	got := toolResultContent(t, res.History, "call1")
	if !strings.Contains(got, "not available to this agent") || !strings.Contains(got, "grep") {
		t.Fatalf("RoleTool Content = %q, want the not-available message naming grep", got)
	}
	if !strings.HasPrefix(got, "error: ") {
		t.Fatalf("RoleTool Content = %q, want the error: prefix", got)
	}
}

func TestRunAgentLoopOnce_UnadmittedHandlerFiresForNonRegistryCall(t *testing.T) {
	comp := &scriptCompleter{steps: toolErrorScript()}
	reg := tools.NewRegistry()
	reg.Register(noopTool{})
	loop := &Loop{Completer: comp, Tools: reg}
	ran := false
	opts := Options{
		Model:    "m",
		MaxSteps: 3,
		UnadmittedToolHandler: func(ctx context.Context, name string) (string, bool) {
			ran = true
			if name == "grep" {
				return "tool is advertised but not admitted", true
			}
			return "", false
		},
	}
	res, err := RunAgentLoopOnce(context.Background(), loop, opts, []provider.Message{{Role: provider.RoleUser, Content: "q"}})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ran {
		t.Fatalf("UnadmittedToolHandler did not run")
	}
	if got := toolResultContent(t, res.History, "call1"); got != "error: tool is advertised but not admitted" {
		t.Fatalf("RoleTool Content = %q, want %q", got, "error: tool is advertised but not admitted")
	}
}
