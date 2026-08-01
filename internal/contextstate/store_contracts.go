package contextstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

type CommitRequest struct {
	OperationID       string           `json:"operation_id"`
	Principal         Principal        `json:"principal"`
	SessionID         string           `json:"session_id"`
	Expected          Revision         `json:"expected"`
	ExpectedBinding   BindingRevision  `json:"expected_binding"`
	NewSourceEvents   []SourceEvent    `json:"new_source_events"`
	Payloads          []PayloadRecord  `json:"payloads,omitempty"`
	Checkpoint        CheckpointRecord `json:"checkpoint"`
	ActiveContext     []byte           `json:"active_context"`
	NewSession        uint64           `json:"new_session"`
	NewDurable        uint64           `json:"new_durable"`
	NewSourceSequence uint64           `json:"new_source_sequence"`
	NewBinding        BindingRevision  `json:"new_binding"`
	TurnID            uint64           `json:"turn_id"`
	BaseDigest        string           `json:"base_digest"`
	Fingerprint       string           `json:"fingerprint"`
}

type EnsureSessionRequest struct {
	Principal Principal       `json:"principal"`
	Binding   BindingRevision `json:"binding"`
}

func NewCommitRequest(principal Principal, sessionID string, expected Revision, expectedBinding BindingRevision, events []SourceEvent, checkpoint CheckpointRecord, activeContext []byte, newBinding BindingRevision, turnID uint64) (CommitRequest, error) {
	checkpoint.Complete = true
	digest := sha256.Sum256(activeContext)
	r := CommitRequest{OperationID: checkpoint.ID.IdempotencyKey, Principal: principal, SessionID: sessionID, Expected: expected, ExpectedBinding: expectedBinding, NewSourceEvents: events, Checkpoint: checkpoint, ActiveContext: activeContext, NewSession: expected.Session + 1, NewDurable: expected.Durable + 1, NewSourceSequence: expected.Source + uint64(len(events)), NewBinding: newBinding, TurnID: turnID, BaseDigest: hex.EncodeToString(digest[:])}
	var err error
	r.Fingerprint, err = FingerprintCommitRequest(r)
	if err != nil {
		return CommitRequest{}, err
	}
	return r, r.Validate()
}

func (r CommitRequest) Validate() error { return validateCommitRequest(r) }

// FingerprintCommitRequest hashes the canonical request fields while omitting
// the fingerprint itself. Storage uses it to distinguish safe retries from a
// reused operation key carrying different state.
func FingerprintCommitRequest(r CommitRequest) (string, error) {
	r.Fingerprint = ""
	data, err := MarshalCanonical(r)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

type AdvanceRequest struct {
	OperationID        string          `json:"operation_id"`
	Principal          Principal       `json:"principal"`
	SessionID          string          `json:"session_id"`
	Expected           Revision        `json:"expected"`
	ExpectedBinding    BindingRevision `json:"expected_binding"`
	NewSession         uint64          `json:"new_session"`
	NewDurable         uint64          `json:"new_durable"`
	NewSourceSequence  uint64          `json:"new_source_sequence"`
	NewBinding         BindingRevision `json:"new_binding"`
	ActiveCheckpointID string          `json:"active_checkpoint_id,omitempty"`
	ClearActive        bool            `json:"clear_active"`
	Reason             string          `json:"reason"`
}

func (r AdvanceRequest) Validate() error {
	if err := r.Principal.Validate(); err != nil {
		return err
	}
	if !r.Principal.IsBound() {
		return invalid("principal", "owner capability is not bound")
	}
	if r.SessionID != r.Principal.SessionID {
		return invalid("session_id", "does not match principal")
	}
	if err := validateIdentifier("operation_id", r.OperationID); err != nil {
		return err
	}
	if err := r.ExpectedBinding.Validate(); err != nil {
		return err
	}
	if err := r.NewBinding.Validate(); err != nil {
		return err
	}
	if r.NewSession != r.Expected.Session+1 || r.NewDurable != r.Expected.Durable+1 || r.NewSourceSequence != r.Expected.Source {
		return invalid("revision", "advance must increment session and durable revisions only")
	}
	if r.ClearActive && r.ActiveCheckpointID != "" {
		return invalid("active_checkpoint_id", "clear and switch are mutually exclusive")
	}
	return validateBoundedText("reason", r.Reason, 256, false)
}

type Store interface {
	EnsureSession(context.Context, EnsureSessionRequest) error
	Commit(context.Context, CommitRequest) error
	Advance(context.Context, AdvanceRequest) error
	Load(context.Context, Principal, string) (Snapshot, error)
}

type SourceReader interface {
	ReadRange(context.Context, Principal, SourceRange) ([]SourceEvent, error)
	ReadPayload(context.Context, Principal, ContentRef) (SanitizedPayload, error)
}
