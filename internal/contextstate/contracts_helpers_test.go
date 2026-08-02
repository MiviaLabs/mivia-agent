package contextstate

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestValidateSourceEvents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		events        []SourceEvent
		sessionID     string
		firstSequence uint64
		wantErr       bool
	}{
		{"valid", []SourceEvent{
			{ID: SourceID{SessionID: "s", Sequence: 1}, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 1},
		}, "s", 1, false},
		{"empty list", []SourceEvent{}, "s", 1, false},
		{"wrong session", []SourceEvent{
			{ID: SourceID{SessionID: "other", Sequence: 1}, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 1},
		}, "s", 1, true},
		{"sequence gap", []SourceEvent{
			{ID: SourceID{SessionID: "s", Sequence: 5}, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 1},
		}, "s", 1, true},
		{"invalid event", []SourceEvent{
			{ID: SourceID{SessionID: "s", Sequence: 1}, Kind: "", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 1},
		}, "s", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSourceEvents(tt.events, tt.sessionID, tt.firstSequence)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSourceEvents() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSourceEventsLimit(t *testing.T) {
	SetLimits(Limits{CommitEvents: 2})
	defer SetLimits(DefaultLimits())

	events := make([]SourceEvent, 3)
	for i := range events {
		events[i] = SourceEvent{
			ID:   SourceID{SessionID: "s", Sequence: uint64(i + 1)},
			Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 1,
		}
	}
	err := ValidateSourceEvents(events, "s", 1)
	if err == nil {
		t.Fatal("expected limit error for too many events")
	}
}

func TestFingerprintCommitRequest(t *testing.T) {
	t.Parallel()
	// Build a valid commit request fixture
	principal, err := NewPrincipal("ws", "s", "sub")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewBindingRevision("p", "m", 1)
	if err != nil {
		t.Fatal(err)
	}
	source, _ := NewSourceID("s", 1)
	rng, _ := NewSourceRange(source, source)
	cid, _ := NewCheckpointID("s", rng, "alg", 1, "m", "key-1")
	checkpoint := CheckpointRecord{
		ID: cid, Revision: Revision{Session: 1, Durable: 1, Source: 1},
		Binding: binding, SourceRange: rng, ActiveContext: []byte(`{}`), TurnID: 1,
	}
	fp1, err := FingerprintCommitRequest(CommitRequest{Principal: principal, SessionID: "s", Checkpoint: checkpoint})
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := FingerprintCommitRequest(CommitRequest{Principal: principal, SessionID: "s", Checkpoint: checkpoint})
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Error("fingerprint should be deterministic")
	}
	// Different content should produce different fingerprint
	checkpoint2 := checkpoint
	checkpoint2.TurnID = 2
	fp3, _ := FingerprintCommitRequest(CommitRequest{Principal: principal, SessionID: "s", Checkpoint: checkpoint2})
	if fp1 == fp3 {
		t.Error("different requests should have different fingerprints")
	}
}

func TestEnsureSessionRequestValidation(t *testing.T) {
	t.Parallel()
	// EnsureSessionRequest itself has no Validate method but the fields it carries do.
	// Just verify it compiles and stores fields correctly.
	req := EnsureSessionRequest{
		Principal: Principal{WorkspaceID: "ws", SessionID: "s", SubjectID: "sub"},
	}
	if req.Principal.SessionID != "s" {
		t.Error("session ID mismatch")
	}
}

func TestExceedsLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		size  int
		bound int
		want  bool
	}{
		{"uncapped zero", 100, 0, false},
		{"within", 5, 10, false},
		{"equal", 10, 10, false},
		{"exceeds", 11, 10, true},
		{"negative bound uncapped", 100, -5, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exceedsLimit(tt.size, tt.bound); got != tt.want {
				t.Errorf("exceedsLimit(%d, %d) = %v, want %v", tt.size, tt.bound, got, tt.want)
			}
		})
	}
}

func sha256HexStr(data []byte) string {
	d := sha256.Sum256(data)
	return hex.EncodeToString(d[:])
}
