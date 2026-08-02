package agent

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// multiStepElisionPrep returns a compacting preparation on the first call and
// a non-compacting one afterward, simulating elide-then-fit within one turn.
type multiStepElisionPrep struct {
	calls int
}

func (p *multiStepElisionPrep) Prepare(_ context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.calls++
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	compacted := p.calls == 1
	prep, err := contextmgr.CapturePreparation(input, contextmgr.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, input.Messages, compacted, "elision-accum-test")
	if err != nil {
		return contextmgr.Preparation{}, err
	}
	if compacted {
		prep.BeforeTokens = 1000
		prep.AfterTokens = 400
		prep.ElidedMessages = 2
		prep.ElidedBytes = 9000
	} else {
		prep.BeforeTokens = 500
		prep.AfterTokens = 500
	}
	return prep, nil
}

func (p *multiStepElisionPrep) Discard(contextmgr.Preparation) {}

// twoStepCompleter returns a tool call on step 1 and a final answer on step 2.
type twoStepCompleter struct{ step int }

func (c *twoStepCompleter) Name() string { return "two-step" }
func (c *twoStepCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "answer", nil
}
func (c *twoStepCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "answer", nil
}
func (c *twoStepCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	c.step++
	if c.step == 1 {
		call := provider.ToolCall{ID: "t1", Type: "function"}
		call.Function.Name = "echo"
		call.Function.Arguments = `{"text":"hi"}`
		return &provider.Response{
			Content:      "",
			FinishReason: "tool_calls",
			ToolCalls:    []provider.ToolCall{call},
		}, nil
	}
	return &provider.Response{Content: "answer", FinishReason: "stop"}, nil
}

func TestLoopAccumulatesElisionAcrossSteps(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("two-step", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "echo"})

	prep := &multiStepElisionPrep{}
	loop := &Loop{Completer: &twoStepCompleter{}, Tools: reg}
	_, err = loop.Run(context.Background(), "question", Options{
		Model: "model", MaxContextTokens: 100_000, MaxSteps: 5,
		PreparationManager: prep,
		PreparationInput: contextmgr.PrepareInput{
			Budget: 100_000, Principal: principal, Binding: binding,
			Revision: contextstate.Revision{Session: 1, Durable: 1, Source: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prep.calls < 2 {
		t.Fatalf("expected multi-step prepare, got %d calls", prep.calls)
	}
	if !loop.HasPreparation || !loop.LastPreparation.Compacted {
		t.Fatal("final preparation lost turn-level Compacted flag")
	}
	if loop.LastPreparation.BeforeTokens != 1000 {
		t.Fatalf("BeforeTokens=%d, want first compacting 1000", loop.LastPreparation.BeforeTokens)
	}
	// Second step did not compact, so last compacting AfterTokens stays 400.
	if loop.LastPreparation.AfterTokens != 400 {
		t.Fatalf("AfterTokens=%d, want last compacting 400", loop.LastPreparation.AfterTokens)
	}
	if loop.LastPreparation.ElidedMessages != 2 || loop.LastPreparation.ElidedBytes != 9000 {
		t.Fatalf("elision totals = msgs=%d bytes=%d", loop.LastPreparation.ElidedMessages, loop.LastPreparation.ElidedBytes)
	}
}

func TestLoopResetsTurnCompactionOnNewRun(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("context-success", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	// Seed stale turn state as if a prior run left it (should not happen, but
	// Run must clear).
	loop := &Loop{
		Completer: preparationSuccessCompleter{}, Tools: tools.NewRegistry(),
		turnCompacted: true, turnBeforeTokens: 99, turnElidedMessages: 7,
	}
	probe := &agentPreparationProbe{}
	_, err = loop.Run(context.Background(), "question", Options{
		Model: "model", MaxContextTokens: 100, PreparationManager: probe,
		PreparationInput: contextmgr.PrepareInput{Budget: 100, Principal: principal, Binding: binding},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Non-compacting probe: after reset, final prep should not show stale totals.
	if loop.LastPreparation.ElidedMessages != 0 || loop.LastPreparation.BeforeTokens == 99 {
		t.Fatalf("stale turn compaction leaked: %+v", loop.LastPreparation)
	}
	if loop.turnCompacted || loop.turnElidedMessages != 0 {
		t.Fatalf("turn accumulators not reset: compacted=%v elided=%d", loop.turnCompacted, loop.turnElidedMessages)
	}
}
