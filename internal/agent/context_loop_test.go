package agent

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type preparationFailureCompleter struct{ err error }

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

type agentPreparationProbe struct {
	discards int
}

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
	_, err = loop.Run(context.Background(), "question", Options{
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
