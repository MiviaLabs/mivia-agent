package manager

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/context/state"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestPreparationTokenRejectsStaleBinding(t *testing.T) {
	principal, err := state.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := state.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	source, err := state.NewSourceID(principal.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	rng, err := state.NewSourceRange(source, source)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := CapturePreparation(
		PrepareInput{Messages: []provider.Message{{Role: provider.RoleUser, Content: "objective"}}, Budget: 100, Principal: principal, Revision: state.Revision{}, Binding: binding},
		CheckpointCandidate{ActiveContext: []byte(`{"messages":[]}`), SourceRange: rng},
		[]provider.Message{{Role: provider.RoleUser, Content: "objective"}}, false, "operation-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := preparation.ValidateToken(state.Revision{}, state.BindingRevision{Provider: binding.Provider, Model: binding.Model, Generation: 2}); !errors.Is(err, state.ErrStaleBinding) {
		t.Fatalf("stale binding error = %v, want ErrStaleBinding", err)
	}
	if err := preparation.ValidateToken(state.Revision{Session: 1}, binding); !errors.Is(err, state.ErrStaleRevision) {
		t.Fatalf("stale revision error = %v, want ErrStaleRevision", err)
	}
}

func TestSummaryEnvelopeRequiresHostSeal(t *testing.T) {
	var unsealed SummaryEnvelope
	if !errors.Is(unsealed.Validate(), state.ErrInvalidDTO) {
		t.Fatal("unsealed summary envelope was accepted")
	}
	principal, _ := state.NewPrincipal("workspace", "session", "subject")
	source, _ := state.NewSourceID(principal.SessionID, 1)
	rng, _ := state.NewSourceRange(source, source)
	envelope, err := NewSummaryEnvelope(1, "objective", "state", nil, nil, nil, nil, nil, rng, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("host-sealed envelope rejected: %v", err)
	}
}
