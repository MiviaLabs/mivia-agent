package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestWriteBackSDKHistoryDropsEmptyAssistantMessage pins the write-back
// half of the empty-shape repair. Since the ContinueOnStop hook keeps the
// SDK-appended triggering assistant message of a continued empty attempt
// in res.History, writeBackSDKHistory must drop that shape before it
// reaches the carried l.Messages - a message dropped only on the wire
// still fails every later turn's preparation validation. The trim-input
// half of the same repair is pinned by the chat package's
// TestFinishAgentTurn_EmptyResponseDoesNotPoisonNextTurnsPreparation.
func TestWriteBackSDKHistoryDropsEmptyAssistantMessage(t *testing.T) {
	l := &Loop{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}
	res := sdkagentloop.Result{History: []sdkshape.Message{
		{Role: sdkshape.RoleUser, Content: "hi"},
		{Role: sdkshape.RoleAssistant, Content: ""},
		{Role: sdkshape.RoleUser, Content: "[mivia: your previous response was empty]"},
		{Role: sdkshape.RoleAssistant, ToolCalls: []sdkshape.ToolCall{{ID: "call_1", Name: "read_file"}}},
		{Role: sdkshape.RoleAssistant, Content: "final"},
	}}
	l.writeBackSDKHistory(res, 1)
	got := l.Messages
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4 (user, notice, tool-call assistant, final): %+v", len(got), got)
	}
	if got[1].Role != provider.RoleUser || !strings.HasPrefix(got[1].Content, "[mivia:") {
		t.Fatalf("the continuation notice must survive the write-back: %+v", got[1])
	}
	if len(got[2].ToolCalls) == 0 {
		t.Fatalf("an assistant message with tool calls must never be dropped: %+v", got[2])
	}
	if got[3].Content != "final" {
		t.Fatalf("the final answer must survive the write-back: %+v", got[3])
	}
	for _, m := range got {
		if m.Role == provider.RoleAssistant && strings.TrimSpace(m.Content) == "" && len(m.ToolCalls) == 0 {
			t.Fatalf("an empty assistant message survived the write-back: %+v", got)
		}
	}
}

// TestRunOnceRunsSDKBackend asserts that runOnce always drives the
// SDK-backed loop through runOnceSDK: the fake completer's ChatTurn
// content comes back as the turn result. The legacy pre-SDK engine
// and its Backend flag are gone (loop_dispatch.go); this is the
// dispatcher's only path now.
func TestRunOnceRunsSDKBackend(t *testing.T) {
	loop := &Loop{
		Completer: &fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "sdk-output", FinishReason: "stop"}},
		Tools:     tools.NewRegistry(),
	}
	got, err := loop.runOnce(context.Background(), "hi", Options{Model: "m", MaxSteps: 1})
	if err != nil {
		t.Fatalf("sdk path failed: %v", err)
	}
	if got != "sdk-output" {
		t.Fatalf("got %q, want %q", got, "sdk-output")
	}
}

// TestRunOnceSDKCarriesMaxToolCallsThroughRun asserts that
// WorkLimits.MaxToolCalls - once the one CLI Options field the SDK
// path could not carry - now runs to completion through the public
// Run, via the ToolBudget bridge (agentloop_toolbudget.go). See
// TestSDKDefaultBackendEnforcesCumulativeMaxToolCalls
// (agentloop_toolbudget_test.go) for the enforcement path.
func TestRunOnceSDKCarriesMaxToolCallsThroughRun(t *testing.T) {
	loop := &Loop{
		Completer: &fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "sdk-output", FinishReason: "stop"}},
		Tools:     tools.NewRegistry(),
	}
	opts := Options{Model: "m", MaxSteps: 1}
	opts.WorkLimits.MaxToolCalls = 1
	got, err := loop.Run(context.Background(), "hi", opts)
	if err != nil {
		t.Fatalf("Run with WorkLimits.MaxToolCalls: %v, want nil (carried)", err)
	}
	if got != "sdk-output" {
		t.Fatalf("got %q, want %q", got, "sdk-output")
	}
}
