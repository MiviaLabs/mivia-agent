package contextstate

import (
	"bytes"
	"errors"
	"testing"
)

func validContractFixture(t *testing.T) (Principal, BindingRevision, SourceEvent, CheckpointRecord) {
	t.Helper()
	principal, err := NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewSourceID(principal.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	rng, err := NewSourceRange(source, source)
	if err != nil {
		t.Fatal(err)
	}
	checkpointID, err := NewCheckpointID(principal.SessionID, rng, "compact-v1", 1, binding.Model, "operation-1")
	if err != nil {
		t.Fatal(err)
	}
	event := SourceEvent{ID: source, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 3}
	checkpoint := CheckpointRecord{
		ID: checkpointID, Revision: Revision{Session: 1, Durable: 1, Source: 1},
		Binding: binding, SourceRange: rng, ActiveContext: []byte(`{"messages":[]}`), TurnID: 1,
	}
	return principal, binding, event, checkpoint
}

func TestRevisionAndCheckpointContracts(t *testing.T) {
	principal, binding, event, checkpoint := validContractFixture(t)
	request, err := NewCommitRequest(principal, principal.SessionID, Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)
	if err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if request.NewSession != 1 || request.NewDurable != 1 || request.NewSourceSequence != 1 {
		t.Fatalf("unexpected new revision: %+v", request)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("request no longer validates: %v", err)
	}
	canonical, err := MarshalCanonical(checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(canonical, []byte(`"algorithm":"compact-v1"`)) {
		t.Fatalf("canonical checkpoint ID omitted algorithm: %s", canonical)
	}
	if !bytes.Equal(canonical, mustCanonical(t, checkpoint.ID)) {
		t.Fatal("canonical serialization is not deterministic")
	}
}

func TestCommitRequestValidation(t *testing.T) {
	principal, binding, event, checkpoint := validContractFixture(t)
	request, err := NewCommitRequest(principal, principal.SessionID, Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)
	if err != nil {
		t.Fatal(err)
	}
	request.SessionID = "foreign-session"
	if err := request.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("session mismatch error = %v, want ErrInvalidDTO", err)
	}
	request.SessionID = principal.SessionID
	request.NewSourceEvents[0].ID.Sequence = 2
	if err := request.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("source gap error = %v, want ErrInvalidDTO", err)
	}
	request.NewSourceEvents[0] = event
	request.Checkpoint.Binding.Generation++
	if err := request.Validate(); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("binding mismatch error = %v, want ErrInvalidDTO", err)
	}
}

func TestCanonicalJSONRejectsDuplicateKeys(t *testing.T) {
	var target map[string]string
	err := UnmarshalCanonical([]byte(`{"a":"one","a":"two"}`), &target)
	if !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("duplicate key error = %v, want ErrInvalidDTO", err)
	}
}

func mustCanonical(t *testing.T, value any) []byte {
	t.Helper()
	data, err := MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
