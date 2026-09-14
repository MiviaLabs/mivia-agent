package agent

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/context/manager"
	"github.com/MiviaLabs/mivia-agent/internal/context/state"
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

func (p *deadlinePreparationProbe) Prepare(ctx context.Context, _ manager.PrepareInput) (manager.Preparation, error) {
	p.calls++
	if ctx.Done() == nil {
		return manager.Preparation{}, errors.New("unexpected background prepare")
	}
	<-ctx.Done()
	return manager.Preparation{}, ctx.Err()
}

func (p *deadlinePreparationProbe) Discard(manager.Preparation) {}

func (p *agentPreparationProbe) Prepare(_ context.Context, input manager.PrepareInput) (manager.Preparation, error) {
	rangeValue := state.SourceRange{
		Start: state.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   state.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	return manager.CapturePreparation(input, manager.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, input.Messages, false, "agent-prep-test")
}

func (p *agentPreparationProbe) Discard(manager.Preparation) { p.discards++ }

func TestAgentTurnDiscardsFailedPreparation(t *testing.T) {
	principal, err := state.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := state.NewBindingRevision("context-test", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	probe := &agentPreparationProbe{}
	loop := &Loop{Completer: &preparationFailureCompleter{err: errPreparationProvider}, Tools: tools.NewRegistry()}
	_, err = loop.Run(context.Background(), "question", Options{Model: "model", MaxContextTokens: 100, PreparationManager: probe,
		PreparationInput: manager.PrepareInput{
			Budget: 100, Principal: principal, Binding: binding,
		},
	})
	if err == nil || !errors.Is(err, errPreparationProvider) {
		t.Fatalf("provider error = %v", err)
	}
	// The loop retains preparation so the session layer can commit an
	// OutcomeUpstreamErr checkpoint; discard is deferred to session lifecycle.
	if probe.discards != 0 || !loop.HasPreparation {
		t.Fatalf("discards=%d hasPreparation=%v, want discards=0 hasPreparation=true", probe.discards, loop.HasPreparation)
	}
}

func TestAgentTurnRetainsSuccessfulPreparationForSessionCommit(t *testing.T) {
	principal, err := state.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := state.NewBindingRevision("context-success", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	probe := &agentPreparationProbe{}
	loop := &Loop{Completer: preparationSuccessCompleter{}, Tools: tools.NewRegistry()}
	_, err = loop.Run(context.Background(), "question", Options{
		Model: "model", MaxContextTokens: 100, PreparationManager: probe,
		PreparationInput: manager.PrepareInput{Budget: 100, Principal: principal, Binding: binding},
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
	_, err := loop.Run(context.Background(), "question", Options{Model: "model", PreparationManager: probe, WorkLimits: runtime.WorkLimits{DeadlineAt: time.Now().Add(20 * time.Millisecond)}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want deadline exceeded", err)
	}
	if probe.calls != 1 {
		t.Fatalf("prepare calls=%d, want one", probe.calls)
	}
}
