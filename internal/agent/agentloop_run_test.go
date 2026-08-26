package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestRunOnceRetriesOnceOnEmptyResponseThenSucceeds pins the automatic
// bounded retry (retryOnEmptyResponse, agentloop_run.go): a completer that
// returns a genuinely empty response (no content, no tool calls -
// sdkagentloop.StopEmptyResponse) on its first call and a real answer on
// its second must succeed with the real answer, not surface "agent: turn
// produced no assistant text" to the caller. This is the fix for having to
// manually retype "continue" after an empty provider response.
func TestRunOnceRetriesOnceOnEmptyResponseThenSucceeds(t *testing.T) {
	calls := 0
	f := &fakeCompleter{name: "fake"}
	f.onChatTurn = func() {
		calls++
		if calls == 1 {
			f.chatTurnOut = &provider.Response{Content: "", FinishReason: "stop"}
		} else {
			f.chatTurnOut = &provider.Response{Content: "real answer", FinishReason: "stop"}
		}
	}
	loop := &Loop{Completer: f, Tools: tools.NewRegistry()}
	got, err := loop.Run(context.Background(), "hi", Options{Model: "m", MaxSteps: 3, RequireFinalText: true})
	if err != nil {
		t.Fatalf("Run: %v, want nil (empty response retried and recovered)", err)
	}
	if got != "real answer" {
		t.Fatalf("got %q, want %q", got, "real answer")
	}
	if calls != 2 {
		t.Fatalf("completer called %d times, want exactly 2 (one empty, one retry)", calls)
	}
}

// TestRunOnceGivesUpAfterExhaustingEmptyResponseRetries pins the bound: a
// completer that ALWAYS returns an empty response must still fail with the
// original error after maxEmptyResponseRetries retries, not retry forever.
func TestRunOnceGivesUpAfterExhaustingEmptyResponseRetries(t *testing.T) {
	calls := 0
	f := &fakeCompleter{
		name:        "fake",
		chatTurnOut: &provider.Response{Content: "", FinishReason: "stop"},
	}
	f.onChatTurn = func() { calls++ }
	loop := &Loop{Completer: f, Tools: tools.NewRegistry()}
	_, err := loop.Run(context.Background(), "hi", Options{Model: "m", MaxSteps: 3, RequireFinalText: true})
	if err == nil || !strings.Contains(err.Error(), "no assistant text") {
		t.Fatalf("Run error = %v, want a 'no assistant text' failure after exhausting retries", err)
	}
	want := 1 + maxEmptyResponseRetries
	if calls != want {
		t.Fatalf("completer called %d times, want exactly %d (1 initial + %d retries)", calls, want, maxEmptyResponseRetries)
	}
}

// TestRunOnceSkipsEmptyResponseRetryWhenProviderReplayDisabled confirms
// DisableProviderReplay suppresses the retry the same way it already
// suppresses the prompt-too-long retry, since both replay the same
// rejected/empty request to the provider.
func TestRunOnceSkipsEmptyResponseRetryWhenProviderReplayDisabled(t *testing.T) {
	calls := 0
	f := &fakeCompleter{
		name:        "fake",
		chatTurnOut: &provider.Response{Content: "", FinishReason: "stop"},
	}
	f.onChatTurn = func() { calls++ }
	loop := &Loop{Completer: f, Tools: tools.NewRegistry()}
	opts := Options{Model: "m", MaxSteps: 3, DisableProviderReplay: true, RequireFinalText: true}
	_, err := loop.Run(context.Background(), "hi", opts)
	if err == nil || !strings.Contains(err.Error(), "no assistant text") {
		t.Fatalf("Run error = %v, want a 'no assistant text' failure with no retry", err)
	}
	if calls != 1 {
		t.Fatalf("completer called %d times, want exactly 1 (no retry when DisableProviderReplay is set)", calls)
	}
}
