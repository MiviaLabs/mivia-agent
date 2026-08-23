package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// The SDK-backed RunAgentLoopOnce must recover from a provider
// prompt-too-long rejection exactly like the legacy loop: compact the
// history to the fixed 16K target, append the model-visible notice, emit
// one EventPrune, and retry the turn exactly once.

// TestSDKRetriesOnceAfterPromptTooLongWithCompaction mirrors
// TestAgentRetriesOnceAfterPromptTooLongWithCompaction on the SDK
// backend: one rejection, one compacted retry, run completes, and the
// retried request carries a history below the 16K target and strictly
// smaller than the rejected one.
func TestSDKRetriesOnceAfterPromptTooLongWithCompaction(t *testing.T) {
	history := buildOversizedHistory()
	beforeTokens := provider.MessagesTokens(history, provider.ContextAccountingProfile{})
	if beforeTokens <= 16<<10 {
		t.Fatalf("test history too small to exercise pruning: %d tokens", beforeTokens)
	}

	comp := &promptTooLongCompleter{
		failN:            1,
		promptTooLongErr: fmt.Errorf("deepseek: provider error (HTTP 400, type invalid_request_error): %w", provider.ErrPromptTooLong),
		steps:            []provider.Response{{Content: "recovered", FinishReason: "stop"}},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: history}

	var prunedEvents []Event
	text, err := loop.Run(context.Background(), "final question", Options{Backend: "sdk",
		Model:    "deepseek-v4-flash",
		MaxSteps: 5,
		OnEvent: func(e Event) {
			if e.Kind == EventPrune {
				prunedEvents = append(prunedEvents, e)
			}
		},
	})
	if err != nil {
		t.Fatalf("run failed after one compaction retry: %v", err)
	}
	if text != "recovered" {
		t.Fatalf("text = %q, want %q", text, "recovered")
	}
	if comp.calls != 2 {
		t.Fatalf("completer called %d times, want exactly 2 (one fail + one retry)", comp.calls)
	}

	retryTokens := provider.MessagesTokens(comp.lastReq.Messages, provider.ContextAccountingProfile{})
	if retryTokens >= 16<<10 {
		t.Fatalf("retry history not compacted to 16K target: %d tokens", retryTokens)
	}
	if retryTokens >= beforeTokens {
		t.Fatalf("retry history not pruned: before=%d after=%d", beforeTokens, retryTokens)
	}
	// The system prompt must survive the compaction.
	if len(loop.Messages) == 0 || loop.Messages[0].Role != provider.RoleSystem || loop.Messages[0].Content != "you are a coding assistant" {
		t.Fatalf("system prompt lost during compaction: %+v", loop.Messages)
	}
	if len(prunedEvents) != 1 || !strings.Contains(prunedEvents[0].Detail, "compacted to 16384 tokens and retrying once") {
		t.Fatalf("expected one compaction prune event, got %+v", prunedEvents)
	}
}

// TestSDKPromptTooLongFailsFastAfterOneRetry mirrors
// TestAgentPromptTooLongFailsFastAfterOneRetry: a rejection that
// survives the single compacted retry fails fast with an error wrapping
// ErrPromptTooLong, and the completer is called exactly twice.
func TestSDKPromptTooLongFailsFastAfterOneRetry(t *testing.T) {
	comp := &promptTooLongCompleter{
		failN:            100,
		promptTooLongErr: fmt.Errorf("deepseek: provider error (HTTP 400, type invalid_request_error): %w", provider.ErrPromptTooLong),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}

	_, err := loop.Run(context.Background(), "final question", Options{Backend: "sdk", Model: "deepseek-v4-flash", MaxSteps: 5})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, provider.ErrPromptTooLong) {
		t.Fatalf("expected error wrapping ErrPromptTooLong, got: %v", err)
	}
	if comp.calls != 2 {
		t.Fatalf("completer called %d times, want exactly 2 (bounded retry, no loop)", comp.calls)
	}
}

// TestSDKDoesNotCompactRetryWhenProviderReplayIsDisabled mirrors
// TestAgentDoesNotCompactRetryWhenProviderReplayIsDisabled: the retry is
// a provider replay, so a caller that disabled replay sees the raw
// rejection after exactly one call.
func TestSDKDoesNotCompactRetryWhenProviderReplayIsDisabled(t *testing.T) {
	comp := &promptTooLongCompleter{failN: 1, promptTooLongErr: fmt.Errorf("%w", provider.ErrPromptTooLong)}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}
	_, err := loop.Run(context.Background(), "final question", Options{Backend: "sdk", Model: "deepseek-v4-flash", MaxSteps: 5, DisableProviderReplay: true})
	if !errors.Is(err, provider.ErrPromptTooLong) {
		t.Fatalf("err=%v, want prompt-too-long error", err)
	}
	if comp.calls != 1 {
		t.Fatalf("completer calls=%d, want one", comp.calls)
	}
}
