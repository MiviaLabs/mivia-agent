package contextmgr

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestBuildCommitRequestMapsCompleteTurn(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	source, err := contextstate.NewSourceID(principal.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	rng, err := contextstate.NewSourceRange(source, source)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := CapturePreparation(
		PrepareInput{Messages: []provider.Message{{Role: provider.RoleUser, Content: "old"}}, Budget: 100, Principal: principal, Binding: binding},
		CheckpointCandidate{SourceRange: rng, SummaryMetadata: []byte(`{"version":1}`)},
		[]provider.Message{{Role: provider.RoleSystem, Content: "system"}, {Role: provider.RoleUser, Content: "old"}}, false, "operation-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	result := TurnResult{
		Ordered:      []provider.Message{{Role: provider.RoleUser, Content: "question"}, {Role: provider.RoleAssistant, Content: "answer"}},
		Active:       []provider.Message{{Role: provider.RoleSystem, Content: "system"}, {Role: provider.RoleUser, Content: "question"}, {Role: provider.RoleAssistant, Content: "answer"}},
		SourceEvents: []contextstate.SourceEvent{{ID: source, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 8}},
		TurnID:       7, Outcome: OutcomeComplete,
	}
	request, err := BuildCommitRequest(nil, preparation, result, principal, contextstate.Revision{}, binding)
	if err != nil {
		t.Fatal(err)
	}
	if request.OperationID != "operation-1" || request.TurnID != 7 || request.NewSourceSequence != 1 {
		t.Fatalf("unexpected request mapping: %+v", request)
	}
	if !request.Checkpoint.Complete || request.Checkpoint.SourceRange != rng {
		t.Fatalf("checkpoint is not complete or has wrong range: %+v", request.Checkpoint)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("mapped request rejected: %v", err)
	}
}

func TestBuildCommitRequestRejectsPrincipalAndToolShapeViolations(t *testing.T) {
	principal, _ := contextstate.NewPrincipal("workspace", "session", "subject")
	binding, _ := contextstate.NewBindingRevision("provider", "model", 1)
	source, _ := contextstate.NewSourceID(principal.SessionID, 1)
	rng, _ := contextstate.NewSourceRange(source, source)
	preparation, err := CapturePreparation(
		PrepareInput{Messages: []provider.Message{{Role: provider.RoleUser, Content: "old"}}, Budget: 100, Principal: principal, Binding: binding},
		CheckpointCandidate{SourceRange: rng}, []provider.Message{{Role: provider.RoleUser, Content: "old"}}, false, "operation-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	result := TurnResult{Ordered: []provider.Message{{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1"}}}}, Active: []provider.Message{{Role: provider.RoleUser, Content: "q"}}, TurnID: 1, Outcome: OutcomeComplete}
	foreign, _ := contextstate.NewPrincipal("other-workspace", "session", "subject")
	if _, err := BuildCommitRequest(nil, preparation, result, foreign, contextstate.Revision{}, binding); !errors.Is(err, contextstate.ErrPrincipalMismatch) {
		t.Fatalf("principal error = %v, want ErrPrincipalMismatch", err)
	}
	if _, err := BuildCommitRequest(nil, preparation, result, principal, contextstate.Revision{}, binding); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("tool shape error = %v, want ErrInvalidDTO", err)
	}
}
