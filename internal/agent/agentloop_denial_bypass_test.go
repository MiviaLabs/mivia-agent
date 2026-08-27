package agent

// Regression-pin for the documented host-vs-SDK shape-bypass on the SDK
// tool-denial path.
//
// Contract pinned here: with BatchResultBudgetBytes > 0 (so the host-side
// turnShapeWrapper.Run in internal/agent/sdk_shaping.go is armed), the
// RoleTool entry produced for a tool-denial case still carries the full
// "error: " + StagedToolMessage body - i.e. the denial content is NOT
// shaped by the per-turn byte budget.
//
// Why this is structurally true, not coincidentally true: the SDK's
// agentloop.runOneToolCall produces the denial Message inside
// toolErrorReportMessage (mivia-ai-sdk/agentloop/toolcall.go:322-333),
// invoked BEFORE the tool's host wrapper would otherwise see a result;
// the host's turnShapeWrapper.Run at internal/agent/sdk_shaping.go:78-137
// only sees bodies that round-trip through inner.Run successfully, so
// denial bodies bypass it by construction. The agentloop_tool_error.go
// preamble documents this divergence (reviewer amendment 6) and asks
// that the SDK path skip legacy BatchResultBudgetBytes / MaxToolResultChars
// shaping for short fixed denial notices.
//
// This test catches a future reader trying to "fix" the asymmetry by
// routing the OnToolCallError Message through the host shaper: doing so
// would cap the 900-byte staged notice to the budget and surface a
// truncated denial to the model. Pin the body intact.

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestRunAgentLoopOnce_StagedDenialBypassesTurnShaping pins the
// structural bypass: a 900-byte staged notice for a tool the registry
// does NOT carry is delivered to the RoleTool history entry in full
// even though BatchResultBudgetBytes arms the host-side turn shaping
// wrapper. Any code path that starts running the denial Message
// through turnShapeWrapper.Run - for example, by constructing the
// denial inside the wrapper layer instead of the SDK's
// toolErrorReportMessage - will cap the notice at the budget and
// this test will fail with the truncated prefix.
func TestRunAgentLoopOnce_StagedDenialBypassesTurnShaping(t *testing.T) {
	// Tight budget armed via the host-side turnShapeWrapper; any
	// shaper that intercepts the denial body would clamp it well
	// below the 900-byte notice.
	const budget = 1024
	const toolName = "long_staged_tool"
	// ~900-byte body, well over half the budget if a shaper charged
	// "remaining" against len(notice); staying under 1024 here would
	// still let a shaper that simply truncates at the budget fire.
	noticeBody := strings.Repeat("z", 900)

	// Script: one SDK turn requests a call to the staged (but
	// unregistered) tool, the next completer step answers with prose.
	comp := &scriptCompleter{steps: []provider.Response{
		{ToolCalls: []provider.ToolCall{tc("call1", toolName, `{}`)}, FinishReason: "tool_calls"},
		{Content: "done", FinishReason: "stop"},
	}}
	// Registry holds an unrelated tool so the loop is non-empty,
	// but NOT the name on the wire - the SDK's decodeAndRun returns
	// tools.ErrUnknownName, which triggers OnToolCallError above.
	reg := tools.NewRegistry()
	reg.Register(noopTool{})

	loop := &Loop{Completer: comp, Tools: reg}
	opts := Options{
		Model:    "m",
		MaxSteps: 3,
		// Exactly the knob that arms turnShapeWrapper for the
		// successful-result path. The OnToolCallError path must not
		// see it.
		BatchResultBudgetBytes: budget,
		// StagedToolMessage answers true for the missing name; the
		// SDK hook (sdkToolCallErrorReporter in agentloop_tool_error.go)
		// renders this body as "error: <body>" verbatim.
		StagedToolMessage: func(name string) (string, bool) {
			if name == toolName {
				return noticeBody, true
			}
			return "", false
		},
	}

	res, err := RunAgentLoopOnce(context.Background(), loop, opts, []provider.Message{{Role: provider.RoleUser, Content: "q"}})
	if err != nil {
		t.Fatalf("RunAgentLoopOnce err = %v, want nil", err)
	}

	got := denialRoleToolContent(t, res.History, "call1")
	want := "error: " + noticeBody
	if got != want {
		t.Fatalf("RoleTool Content = %q (len=%d), want %q (len=%d) - denial body was shaped by the turn budget, the structural bypass regressed",
			got, len(got), want, len(want))
	}
	// Belt and braces: nothing in the captured history should carry
	// a truncation marker - the host never attempted to shape the
	// notice, so no notice-tag should appear.
	if strings.Contains(got, "[tool-error]") || strings.Contains(got, "truncated") || strings.Contains(got, "byte") {
		t.Fatalf("RoleTool Content carries a shaping marker that the host-side shaper would emit: %q", got)
	}
}

// denialRoleToolContent finds the RoleTool history entry for callID
// and returns its Content, failing the test when absent. Mirrors the
// helper in agentloop_tool_error_test.go and is kept inline (not
// shared) so this file stays self-explanatory as a one-file pin.
func denialRoleToolContent(t *testing.T, history []sdkshape.Message, callID string) string {
	t.Helper()
	for _, m := range history {
		if m.Role == sdkshape.RoleTool && m.ToolCallID == callID {
			return m.Content
		}
	}
	t.Fatalf("no RoleTool message with ToolCallID %q in history", callID)
	return ""
}
