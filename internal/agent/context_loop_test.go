package agent

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type preparationFailureCompleter struct{ err error }

type preparationSuccessCompleter struct{}

var errPreparationProvider = errors.New("provider failed")

func (c *preparationFailureCompleter) Name() string { return "context-test" }
func (c *preparationFailureCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", c.err
}
func (c *preparationFailureCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", c.err
}
func (c *preparationFailureCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return nil, c.err
}

func (preparationSuccessCompleter) Name() string { return "context-success" }
func (preparationSuccessCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "answer", nil
}
func (preparationSuccessCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "answer", nil
}
func (preparationSuccessCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "answer", FinishReason: "stop"}, nil
}

type agentPreparationProbe struct {
	discards int
}

type deadlinePreparationProbe struct{ calls int }

func (p *deadlinePreparationProbe) Prepare(ctx context.Context, _ contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.calls++
	if ctx.Done() == nil {
		return contextmgr.Preparation{}, errors.New("unexpected background prepare")
	}
	<-ctx.Done()
	return contextmgr.Preparation{}, ctx.Err()
}

func (p *deadlinePreparationProbe) Discard(contextmgr.Preparation) {}

func (p *agentPreparationProbe) Prepare(_ context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	return contextmgr.CapturePreparation(input, contextmgr.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, input.Messages, false, "agent-prep-test")
}

func (p *agentPreparationProbe) Discard(contextmgr.Preparation) { p.discards++ }

func TestAgentTurnDiscardsFailedPreparation(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("context-test", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	probe := &agentPreparationProbe{}
	loop := &Loop{Completer: &preparationFailureCompleter{err: errPreparationProvider}, Tools: tools.NewRegistry()}
	_, err = loop.Run(context.Background(), "question", Options{Backend: "legacy",
		Model: "model", MaxContextTokens: 100, PreparationManager: probe,
		PreparationInput: contextmgr.PrepareInput{
			Budget: 100, Principal: principal, Binding: binding,
		},
	})
	if err == nil || !errors.Is(err, errPreparationProvider) {
		t.Fatalf("provider error = %v", err)
	}
	if probe.discards != 1 || loop.HasPreparation {
		t.Fatalf("discards=%d hasPreparation=%v", probe.discards, loop.HasPreparation)
	}
}

func TestAgentTurnRetainsSuccessfulPreparationForSessionCommit(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("context-success", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	probe := &agentPreparationProbe{}
	loop := &Loop{Completer: preparationSuccessCompleter{}, Tools: tools.NewRegistry()}
	_, err = loop.Run(context.Background(), "question", Options{
		Model: "model", MaxContextTokens: 100, PreparationManager: probe,
		PreparationInput: contextmgr.PrepareInput{Budget: 100, Principal: principal, Binding: binding},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !loop.HasPreparation {
		t.Fatal("successful agent turn discarded preparation before the session could commit it")
	}
	loop.discardPreparation(Options{PreparationManager: probe})
	if probe.discards != 1 {
		t.Fatalf("discards=%d, want one owner cleanup", probe.discards)
	}
}

func TestAgentDeadlineDoesNotUseBackgroundPreparationFallback(t *testing.T) {
	probe := &deadlinePreparationProbe{}
	loop := &Loop{Completer: preparationSuccessCompleter{}, Tools: tools.NewRegistry()}
	_, err := loop.Run(context.Background(), "question", Options{Backend: "legacy", Model: "model", PreparationManager: probe, WorkLimits: runtime.WorkLimits{DeadlineAt: time.Now().Add(20 * time.Millisecond)}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want deadline exceeded", err)
	}
	if probe.calls != 1 {
		t.Fatalf("prepare calls=%d, want one", probe.calls)
	}
}
