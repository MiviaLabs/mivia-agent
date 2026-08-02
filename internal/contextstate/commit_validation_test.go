package contextstate

import (
	"testing"
)

func TestValidateCommitRequestBasic(t *testing.T) {
	t.Parallel()
	principal, err := NewPrincipal("ws", "s", "sub")
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := NewBindingRevision("p", "m", 1)
	source, _ := NewSourceID("s", 1)
	rng, _ := NewSourceRange(source, source)
	cid, _ := NewCheckpointID("s", rng, "alg", 1, "m", "op-1")
	event := SourceEvent{ID: source, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 3}
	checkpoint := CheckpointRecord{
		ID: cid, Revision: Revision{Session: 1, Durable: 1, Source: 1},
		Binding: binding, SourceRange: rng, ActiveContext: []byte(`{"m":[]}`), TurnID: 1,
	}

	req, err := NewCommitRequest(principal, "s", Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)
	if err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("valid request re-validate: %v", err)
	}
}

func TestValidateCommitIdentityErrors(t *testing.T) {
	t.Parallel()
	principal, _ := NewPrincipal("ws", "s", "sub")
	binding, _ := NewBindingRevision("p", "m", 1)
	source, _ := NewSourceID("s", 1)
	rng, _ := NewSourceRange(source, source)
	cid, _ := NewCheckpointID("s", rng, "alg", 1, "m", "op-1")
	event := SourceEvent{ID: source, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 3}
	checkpoint := CheckpointRecord{
		ID: cid, Revision: Revision{Session: 1, Durable: 1, Source: 1},
		Binding: binding, SourceRange: rng, ActiveContext: []byte(`{"m":[]}`), TurnID: 1,
	}
	req, _ := NewCommitRequest(principal, "s", Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)

	// Session ID mismatch
	orig := req.SessionID
	req.SessionID = "other"
	if err := validateCommitIdentity(req); err == nil {
		t.Error("expected error for session mismatch")
	}
	req.SessionID = orig

	// Invalid base digest
	req.BaseDigest = "invalid"
	if err := validateCommitIdentity(req); err == nil {
		t.Error("expected error for invalid base digest")
	}
	// Restore
	req, _ = NewCommitRequest(principal, "s", Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)

	// Invalid fingerprint
	req.Fingerprint = "a"
	if err := validateCommitIdentity(req); err == nil {
		t.Error("expected error for mismatched fingerprint")
	}
}

func TestValidateCommitRevisionErrors(t *testing.T) {
	t.Parallel()
	principal, _ := NewPrincipal("ws", "s", "sub")
	binding, _ := NewBindingRevision("p", "m", 1)
	source, _ := NewSourceID("s", 1)
	rng, _ := NewSourceRange(source, source)
	cid, _ := NewCheckpointID("s", rng, "alg", 1, "m", "op-1")
	event := SourceEvent{ID: source, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 3}
	checkpoint := CheckpointRecord{
		ID: cid, Revision: Revision{Session: 1, Durable: 1, Source: 1},
		Binding: binding, SourceRange: rng, ActiveContext: []byte(`{"m":[]}`), TurnID: 1,
	}
	req, _ := NewCommitRequest(principal, "s", Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)

	// Wrong new revision
	req.NewSession = 99
	if err := validateCommitRevision(req); err == nil {
		t.Error("expected error for wrong new revision")
	}

	// Sequence doesn't match event count
	req, _ = NewCommitRequest(principal, "s", Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)
	req.NewSourceSequence = 99
	if err := validateCommitRevision(req); err == nil {
		t.Error("expected error for sequence mismatch")
	}
}

func TestValidateCommitEventsErrors(t *testing.T) {
	t.Parallel()
	principal, _ := NewPrincipal("ws", "s", "sub")
	binding, _ := NewBindingRevision("p", "m", 1)
	source, _ := NewSourceID("s", 1)
	rng, _ := NewSourceRange(source, source)
	cid, _ := NewCheckpointID("s", rng, "alg", 1, "m", "op-1")
	event := SourceEvent{ID: source, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 3}
	checkpoint := CheckpointRecord{
		ID: cid, Revision: Revision{Session: 1, Durable: 1, Source: 1},
		Binding: binding, SourceRange: rng, ActiveContext: []byte(`{"m":[]}`), TurnID: 1,
	}
	req, _ := NewCommitRequest(principal, "s", Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)

	// Event from wrong session
	req.NewSourceEvents[0].ID.SessionID = "other"
	if err := validateCommitEvents(req); err == nil {
		t.Error("expected error for wrong session event")
	}

	// Sequence not contiguous
	req, _ = NewCommitRequest(principal, "s", Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)
	req.NewSourceEvents[0].ID.Sequence = 99
	if err := validateCommitEvents(req); err == nil {
		t.Error("expected error for non-contiguous sequence")
	}
}

func TestValidateCommitCheckpointErrors(t *testing.T) {
	t.Parallel()
	principal, _ := NewPrincipal("ws", "s", "sub")
	binding, _ := NewBindingRevision("p", "m", 1)
	source, _ := NewSourceID("s", 1)
	rng, _ := NewSourceRange(source, source)
	cid, _ := NewCheckpointID("s", rng, "alg", 1, "m", "op-1")
	event := SourceEvent{ID: source, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 3}
	checkpoint := CheckpointRecord{
		ID: cid, Revision: Revision{Session: 1, Durable: 1, Source: 1},
		Binding: binding, SourceRange: rng, ActiveContext: []byte(`{"m":[]}`), TurnID: 1,
	}
	req, _ := NewCommitRequest(principal, "s", Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)

	// Zero turn ID
	req.TurnID = 0
	if err := validateCommitCheckpoint(req); err == nil {
		t.Error("expected error for zero turn ID")
	}

	// Empty active context
	req, _ = NewCommitRequest(principal, "s", Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)
	req.ActiveContext = nil
	if err := validateCommitCheckpoint(req); err == nil {
		t.Error("expected error for empty active context")
	}
}

func TestValidateCommitPayloadsErrors(t *testing.T) {
	t.Parallel()
	principal, _ := NewPrincipal("ws", "s", "sub")
	binding, _ := NewBindingRevision("p", "m", 1)
	source, _ := NewSourceID("s", 1)
	rng, _ := NewSourceRange(source, source)
	cid, _ := NewCheckpointID("s", rng, "alg", 1, "m", "op-1")
	event := SourceEvent{ID: source, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 3}
	checkpoint := CheckpointRecord{
		ID: cid, Revision: Revision{Session: 1, Durable: 1, Source: 1},
		Binding: binding, SourceRange: rng, ActiveContext: []byte(`{"m":[]}`), TurnID: 1,
	}
	req, _ := NewCommitRequest(principal, "s", Revision{}, binding, []SourceEvent{event}, checkpoint, checkpoint.ActiveContext, binding, 1)

	// Payload with wrong session
	req.Payloads = []PayloadRecord{{
		Ref: ContentRef{
			Ref: "x", Namespace: Namespace,
			SHA256:      sha256HexStr([]byte("data")),
			WorkspaceID: "ws", SessionID: "other", SubjectID: "sub", Size: 4,
		},
		Retention: RetentionSession, Data: []byte("data"),
	}}
	if err := validateCommitPayloads(req); err == nil {
		t.Error("expected error for wrong session payload")
	}
}
