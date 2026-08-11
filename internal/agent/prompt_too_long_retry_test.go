package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// promptTooLongCompleter is a scripted completer that fails the first failN
// calls with a prompt-too-long error (the shape a provider adapter surfaces
// after wrapping ErrPromptTooLong) and then answers from a script. It records
// every request's messages so tests can assert the retry carried a compacted
// history.
type promptTooLongCompleter struct {
	calls            int
	failN            int
	promptTooLongErr error
	steps            []provider.Response
	lastReq          provider.Request
}

func (c *promptTooLongCompleter) Name() string { return "script" }
func (c *promptTooLongCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *promptTooLongCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *promptTooLongCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.calls++
	c.lastReq = req
	if c.calls <= c.failN {
		return nil, c.promptTooLongErr
	}
	if idx := c.calls - c.failN - 1; idx >= 0 && idx < len(c.steps) {
		r := c.steps[idx]
		return &r, nil
	}
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

// fillTokens builds a message whose estimated token count is roughly wantTok
// (the len/4 heuristic), sized so turns stay individually under the 16K retry
// target and the whole history exceeds it - that is the shape that actually
// exercises turn-granular pruning.
func fillTokens(role, text string, wantTokens int) provider.Message {
	return provider.Message{Role: role, Content: text + strings.Repeat("x", wantTokens*4-len(text))}
}

// buildOversizedHistory returns a history whose total estimated tokens exceed
// the 16K retry target while every turn stays under it, so a compact-and-retry
// must drop older turns to fit.
func buildOversizedHistory() []provider.Message {
	const perTurnTokens = 6000 // two messages x 3000 tokens = one 6000-token turn
	repeat := perTurnTokens / 2
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "you are a coding assistant"},
		fillTokens(provider.RoleUser, "turn one user", repeat),
		fillTokens(provider.RoleAssistant, "turn one assistant", repeat),
		fillTokens(provider.RoleUser, "turn two user", repeat),
		fillTokens(provider.RoleAssistant, "turn two assistant", repeat),
		fillTokens(provider.RoleUser, "turn three user", repeat),
		fillTokens(provider.RoleAssistant, "turn three assistant", repeat),
	}
}

// A provider prompt-too-long rejection is recovered exactly once: the history
// is compacted to the fixed 16K target and the call is retried, after which the
// run completes normally with a bounded number of provider calls.
func TestAgentRetriesOnceAfterPromptTooLongWithCompaction(t *testing.T) {
	history := buildOversizedHistory()
	beforeTokens := provider.MessagesTokens(history)
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
	text, err := loop.Run(context.Background(), "final question", Options{
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

	// The retry must carry the compacted history: below the 16K target and
	// strictly smaller than what the provider rejected.
	retryTokens := provider.MessagesTokens(comp.lastReq.Messages)
	if retryTokens >= 16<<10 {
		t.Fatalf("retry history not compacted to 16K target: %d tokens", retryTokens)
	}
	if retryTokens >= beforeTokens {
		t.Fatalf("retry history not pruned: before=%d after=%d", beforeTokens, retryTokens)
	}
	// Compaction must keep the system prompt and the newest turns.
	if len(loop.Messages) == 0 || loop.Messages[0].Role != provider.RoleSystem || loop.Messages[0].Content != "you are a coding assistant" {
		t.Fatalf("system prompt lost during compaction: %+v", loop.Messages)
	}
	if len(prunedEvents) != 1 || !strings.Contains(prunedEvents[0].Detail, "compacted to 16384 tokens and retrying once") {
		t.Fatalf("expected one compaction prune event, got %+v", prunedEvents)
	}
}

// A prompt-too-long rejection that survives the single retry must fail fast:
// the error wraps ErrPromptTooLong, and the completer is called exactly twice
// (never an unbounded retry loop).
func TestAgentPromptTooLongFailsFastAfterOneRetry(t *testing.T) {
	comp := &promptTooLongCompleter{
		failN:            100,
		promptTooLongErr: fmt.Errorf("deepseek: provider error (HTTP 400, type invalid_request_error): %w", provider.ErrPromptTooLong),
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}

	_, err := loop.Run(context.Background(), "final question", Options{Model: "deepseek-v4-flash", MaxSteps: 5})
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

func TestAgentDoesNotCompactRetryWhenProviderReplayIsDisabled(t *testing.T) {
	comp := &promptTooLongCompleter{failN: 1, promptTooLongErr: fmt.Errorf("%w", provider.ErrPromptTooLong)}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}
	_, err := loop.Run(context.Background(), "final question", Options{Model: "deepseek-v4-flash", MaxSteps: 5, DisableProviderReplay: true})
	if !errors.Is(err, provider.ErrPromptTooLong) {
		t.Fatalf("err=%v, want prompt-too-long error", err)
	}
	if comp.calls != 1 {
		t.Fatalf("completer calls=%d, want one", comp.calls)
	}
}

// A prompt-too-long rejection followed by one compaction retry must charge the
// output allowance exactly once. requestStep reserves prompt+output before the
// rejected first call; the retry's output was already reserved by that first
// attempt, so the retry may charge only its (new, compacted) prompt. Charging
// the full per-call output twice makes a finite MaxOutputTokens budget hard-fail
// with "work limit exceeded: output tokens" even though only one completion
// ever happened (DC-6 broken bound on the DC-8 retry path).
func TestPromptTooLongRetryDoesNotDoubleChargeOutputBudget(t *testing.T) {
	const outputBudget = 4096
	maxTokens := outputBudget
	comp := &promptTooLongCompleter{
		failN:            1,
		promptTooLongErr: fmt.Errorf("deepseek: provider error (HTTP 400, type invalid_request_error): %w", provider.ErrPromptTooLong),
		steps:            []provider.Response{{Content: "recovered", FinishReason: "stop"}},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}

	text, err := loop.Run(context.Background(), "final question", Options{
		Model:      "deepseek-v4-flash",
		MaxTokens:  &maxTokens,
		WorkLimits: runtime.WorkLimits{MaxOutputTokens: outputBudget},
		MaxSteps:   5,
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
	if loop.workLimits.outputTokens != outputBudget {
		t.Fatalf("meter outputTokens = %d, want %d (one logical completion charged once)", loop.workLimits.outputTokens, outputBudget)
	}
}

// The fix must not bypass the cumulative output bound: a MaxOutputTokens that
// genuinely cannot fit one completion still fails the run with the work-limit
// error, before any provider call (stepRequest's outputCap rejects it).
func TestPromptTooLongRetryHonorsTrulyExhaustedOutputBudget(t *testing.T) {
	maxTokens := 8192
	comp := &promptTooLongCompleter{
		failN:            1,
		promptTooLongErr: fmt.Errorf("deepseek: provider error (HTTP 400, type invalid_request_error): %w", provider.ErrPromptTooLong),
		steps:            []provider.Response{{Content: "recovered", FinishReason: "stop"}},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}

	_, err := loop.Run(context.Background(), "final question", Options{
		Model:      "deepseek-v4-flash",
		MaxTokens:  &maxTokens,
		WorkLimits: runtime.WorkLimits{MaxOutputTokens: 512},
		MaxSteps:   5,
	})
	if err == nil {
		t.Fatal("expected work-limit error")
	}
	if !strings.Contains(err.Error(), "work limit exceeded: output tokens") {
		t.Fatalf("err = %v, want %q", err, "work limit exceeded: output tokens")
	}
	if comp.calls != 0 {
		t.Fatalf("completer called %d times, want 0 (output cap rejects before any provider call)", comp.calls)
	}
}
