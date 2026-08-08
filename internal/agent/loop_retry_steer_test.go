package agent

// Soft steer racing a prompt-too-long rejection (plan 54 x prompt-too-long
// recovery). Defect under test: requestStep ran retryAfterPromptTooLong on the
// SAME llmCtx the steer watcher had already canceled, so the retry was always
// doomed (immediate context.Canceled), was mis-mapped to errSteerInterrupt
// (soft continue), and the compaction landed in history but was never
// completed on a live context in that call: one provider invocation and one
// step were wasted re-requesting the compacted history (DC-8 retry on a dead
// context; DC-9 recovery silently deferred). Reachable from the subagent entry
// point: MultiStepHandler.run -> runValidatedReply -> Loop.Run -> requestStep,
// with the steer options wired by subagents.applyMailboxAccess (InterruptCh,
// MailboxPendingInterrupt, SoftInterruptCooldown). Fixed by deriving a fresh
// live context for the retry.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// steerPromptTooLongCompleter scripts a soft steer racing the provider's own
// prompt-too-long rejection. Call 1 blocks until its request context is
// canceled (the steer watcher's llmCancel) and then surfaces a wrapped
// ErrPromptTooLong - a provider that had already validated the prompt as too
// long while the steer fired. Every later call honors a canceled request
// context FIRST (as a real adapter does) and then rejects calls 2..failN with
// ErrPromptTooLong before answering with recovered. Requests and the call
// counter are recorded on the loop goroutine (ChatTurn is synchronous on it).
type steerPromptTooLongCompleter struct {
	calls            int
	failN            int
	promptTooLongErr error
	recovered        string
	requests         []provider.Request
}

func (c *steerPromptTooLongCompleter) Name() string { return "script" }
func (c *steerPromptTooLongCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *steerPromptTooLongCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *steerPromptTooLongCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.requests = append(c.requests, req)
	c.calls++
	if c.calls == 1 {
		// Script the steer racing the provider's own rejection: block until
		// the watcher cancels llmCtx, then surface the provider's
		// prompt-too-long verdict (it validated the prompt before the cancel
		// propagated).
		<-ctx.Done()
		return nil, c.promptTooLongErr
	}
	if ctx.Err() != nil {
		// A real adapter honors a canceled request context before doing
		// anything else; a live-context call proceeds to the script.
		return nil, ctx.Err()
	}
	if c.calls <= c.failN {
		return nil, c.promptTooLongErr
	}
	return &provider.Response{Content: c.recovered, FinishReason: "stop"}, nil
}

// steerPromptTooLongOptions builds the subagent-style steer options both tests
// drive Loop.Run with: an interrupt channel PRELOADED with one steer token,
// MailboxPendingInterrupt true until the BeforeStep drains the mailbox after
// step 1, SoftInterruptCooldown 0 (tests disable the cap), and a context
// budget so the prompt-too-long retry compaction is active.
func steerPromptTooLongOptions(pending *atomic.Bool, stepCalls *int) Options {
	interrupt := make(chan struct{}, 1)
	interrupt <- struct{}{}
	return Options{
		Model: "m", MaxSteps: 10, MaxContextTokens: 20000,
		InterruptCh:             func() <-chan struct{} { return interrupt },
		MailboxPendingInterrupt: func() bool { return pending.Load() },
		SoftInterruptCooldown:   0,
		BeforeStep: func() []provider.Message {
			*stepCalls = *stepCalls + 1
			if *stepCalls > 1 {
				pending.Store(false) // mailbox drained after step 1
			}
			return nil
		},
	}
}

// TestLoopPromptTooLongRetryUsesLiveContextWhenSteerFires is the RED test: a
// soft steer fires during the first provider call and that call is rejected
// with ErrPromptTooLong. The compact-and-retry must complete on a LIVE context
// in the same step - exactly two provider calls (rejected first attempt +
// successful retry) - and the retry request must carry the pruned history plus
// the promptTooLongCompactNotice. Pre-fix, the retry ran on the steer-canceled
// llmCtx, was doomed (context.Canceled -> errSteerInterrupt soft continue),
// and the loop made a third call re-requesting the compacted history.
func TestLoopPromptTooLongRetryUsesLiveContextWhenSteerFires(t *testing.T) {
	var pending atomic.Bool
	pending.Store(true)
	stepCalls := 0
	comp := &steerPromptTooLongCompleter{
		failN:            1,
		promptTooLongErr: fmt.Errorf("deepseek: provider error (HTTP 400, type invalid_request_error): %w", provider.ErrPromptTooLong),
		recovered:        "recovered",
	}
	history := buildOversizedHistory()
	beforeTokens := provider.MessagesTokens(history)
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: history}

	text, err := runLoop(t, loop, context.Background(), "final question",
		steerPromptTooLongOptions(&pending, &stepCalls))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if text != "recovered" {
		t.Fatalf("text=%q, want recovered", text)
	}
	if comp.calls != 2 {
		t.Fatalf("completer called %d times, want exactly 2 (rejected attempt + live-context retry); a steer-canceled llmCtx makes the retry doomed and forces a third re-request", comp.calls)
	}
	if len(comp.requests) != 2 {
		t.Fatalf("recorded requests=%d, want 2", len(comp.requests))
	}
	retryTokens := provider.MessagesTokens(comp.requests[1].Messages)
	if retryTokens >= 16<<10 {
		t.Fatalf("retry request not compacted to the 16K target: %d tokens", retryTokens)
	}
	if retryTokens >= beforeTokens {
		t.Fatalf("retry request not pruned: before=%d retry=%d", beforeTokens, retryTokens)
	}
	found := false
	for _, m := range comp.requests[1].Messages {
		if strings.Contains(m.Content, promptTooLongCompactNotice) {
			found = true
		}
	}
	if !found {
		t.Fatal("retry request missing the prompt-too-long compaction notice")
	}
	if err := provider.ValidateToolPairing(loop.Messages); err != nil {
		t.Fatalf("pairing broken by the compact-and-retry: %v", err)
	}
}

// TestLoopPromptTooLongRetrySecondRejectionFailsTurn is the negative-path pin:
// when the LIVE-context retry is itself rejected with ErrPromptTooLong, the
// turn must fail fast - exactly two provider calls, no second retry, no loop -
// and the error must wrap ErrPromptTooLong. It guards the fix against
// introducing a retry loop. Pre-fix, the doomed retry soft-continued and a
// third call "succeeded", hiding the permanent rejection.
func TestLoopPromptTooLongRetrySecondRejectionFailsTurn(t *testing.T) {
	var pending atomic.Bool
	pending.Store(true)
	stepCalls := 0
	comp := &steerPromptTooLongCompleter{
		failN:            2,
		promptTooLongErr: fmt.Errorf("deepseek: provider error (HTTP 400, type invalid_request_error): %w", provider.ErrPromptTooLong),
		recovered:        "recovered",
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}

	_, err := runLoop(t, loop, context.Background(), "final question",
		steerPromptTooLongOptions(&pending, &stepCalls))
	if err == nil {
		t.Fatal("expected the second prompt-too-long rejection to fail the turn")
	}
	if !errors.Is(err, provider.ErrPromptTooLong) {
		t.Fatalf("err=%v, want an error wrapping provider.ErrPromptTooLong", err)
	}
	if comp.calls != 2 {
		t.Fatalf("completer called %d times, want exactly 2 (rejection + retry rejection; no second retry, no loop)", comp.calls)
	}
}
