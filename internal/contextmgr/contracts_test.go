package contextmgr

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestPreparationTokenRejectsStaleBinding(t *testing.T) {
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
		PrepareInput{Messages: []provider.Message{{Role: provider.RoleUser, Content: "objective"}}, Budget: 100, Principal: principal, Revision: contextstate.Revision{}, Binding: binding},
		CheckpointCandidate{ActiveContext: []byte(`{"messages":[]}`), SourceRange: rng},
		[]provider.Message{{Role: provider.RoleUser, Content: "objective"}}, false, "operation-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := preparation.ValidateToken(contextstate.Revision{}, contextstate.BindingRevision{Provider: binding.Provider, Model: binding.Model, Generation: 2}); !errors.Is(err, contextstate.ErrStaleBinding) {
		t.Fatalf("stale binding error = %v, want ErrStaleBinding", err)
	}
	if err := preparation.ValidateToken(contextstate.Revision{Session: 1}, binding); !errors.Is(err, contextstate.ErrStaleRevision) {
		t.Fatalf("stale revision error = %v, want ErrStaleRevision", err)
	}
}

func TestSummaryEnvelopeRequiresHostSeal(t *testing.T) {
	var unsealed SummaryEnvelope
	if !errors.Is(unsealed.Validate(), contextstate.ErrInvalidDTO) {
		t.Fatal("unsealed summary envelope was accepted")
	}
	principal, _ := contextstate.NewPrincipal("workspace", "session", "subject")
	source, _ := contextstate.NewSourceID(principal.SessionID, 1)
	rng, _ := contextstate.NewSourceRange(source, source)
	envelope, err := NewSummaryEnvelope(1, "objective", "state", nil, nil, nil, nil, nil, rng, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("host-sealed envelope rejected: %v", err)
	}
}
