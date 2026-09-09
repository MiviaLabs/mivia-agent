package agent

import (
	"context"
	"errors"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// alwaysPromptTooLongSDKCompleter always rejects with
// sdkshape.ErrPromptTooLong and counts its own Chat calls, so a test
// can assert how many times the wrapper actually invoked it.
type alwaysPromptTooLongSDKCompleter struct {
	calls int
}

func (c *alwaysPromptTooLongSDKCompleter) Name() string { return "always-prompt-too-long" }

func (c *alwaysPromptTooLongSDKCompleter) Chat(context.Context, sdkshape.Request) (sdkshape.Response, error) {
	c.calls++
	return sdkshape.Response{}, sdkshape.ErrPromptTooLong
}

func (c *alwaysPromptTooLongSDKCompleter) ChatStream(context.Context, sdkshape.Request) (<-chan sdkshape.Chunk, error) {
	return nil, sdkshape.ErrPromptTooLong
}

// TestPromptTooLongOnAdoptedWindowDoesNotRerun is a RED test for a
// fast-bug-audit finding: runSDKPromptTooLongRecoverable always
// reruns the whole SDK loop once on provider.ErrPromptTooLong,
// including on a turn that adopted the SDK's own Window compaction
// (sdkCompactionAdopted true) - whose Window-based recovery already
// ran and failed inside the first run() call. The host rerun is
// wasted work and risks committing state (InjectedSummary,
// LastPreparation.Compacted) grounded by the first, abandoned
// attempt against the second attempt's different, structurally
// pruned history: resetTurnCompaction only runs once per TURN
// (loop_dispatch.go), not between the two run() calls here.
func TestPromptTooLongOnAdoptedWindowDoesNotRerun(t *testing.T) {
	completer := &alwaysPromptTooLongSDKCompleter{}
	sdkOpts := sdkagentloop.Options{
		Completer: completer,
		Tools:     sdktools.New(),
		Model:     "m",
	}
	l := &Loop{}
	opts := Options{
		// sdkCompactionAdopted(opts): ceiling + summarizer set, no
		// PreparationManager - the row that stays automatically
		// adopted in production today.
		MaxContextTokens: 1000,
		SummaryConfig:    SummaryConfig{Summarizer: ptrSummarizer(summaryInjectSummarizer(t, fullSummaryProvider{}))},
	}
	if !sdkCompactionAdopted(opts) {
		t.Fatal("test fixture is not adopted; sdkCompactionAdopted(opts) = false")
	}
	msgs := []sdkshape.Message{{Role: sdkshape.RoleUser, Content: "hi"}}
	_, err := runSDKPromptTooLongRecoverable(context.Background(), l, sdkOpts, opts, msgs, newSDKTurnState())
	if !errors.Is(err, sdkshape.ErrPromptTooLong) {
		t.Fatalf("err = %v, want errors.Is ErrPromptTooLong", err)
	}
	if completer.calls != 1 {
		t.Fatalf("completer.calls = %d, want 1: an adopted turn's SDK Window recovery already ran inside that one call; the host must not rerun", completer.calls)
	}
}
