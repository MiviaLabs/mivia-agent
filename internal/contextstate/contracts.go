// Package contextstate defines the dependency-neutral durable context
// contract. It deliberately knows nothing about providers, chat, or storage.
package contextstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	Namespace                = "mivia.context.payload.v1"
	MaxIdentifierBytes       = 128
	MaxSourceEventBytes      = 8 * 1024
	MaxPayloadReferenceBytes = 256
	MaxSourceRangeEvents     = 100_000
	MaxCheckpointMetadata    = 16 * 1024
	MaxCheckpointBytes       = 32 * 1024
	MaxSessionStateBytes     = 64 * 1024 * 1024
	MaxCommitEvents          = 1_024
	MaxCommitEventBytes      = 8 * 1024 * 1024
	MaxSummaryMetadata       = 12 * 1024
	MaxExportBytes           = 8 * 1024 * 1024
	MaxAuditBytes            = 1 * 1024
)

var (
	ErrInvalidDTO         = errors.New("invalid context DTO")
	ErrPrincipalMismatch  = errors.New("principal mismatch")
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionTombstoned  = errors.New("session tombstoned")
	ErrPayloadRevoked     = errors.New("payload revoked")
	ErrSummaryUnavailable = errors.New("summary unavailable")
	ErrStaleRevision      = errors.New("stale revision")
	ErrStaleBinding       = errors.New("stale binding")
	ErrCheckpointConflict = errors.New("checkpoint conflict")
)

// ValidationError retains the field that made a DTO invalid while allowing
// callers to use errors.Is(err, ErrInvalidDTO).
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ErrInvalidDTO.Error()
	}
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", ErrInvalidDTO, e.Reason)
	}
	return fmt.Sprintf("%s: %s: %s", ErrInvalidDTO, e.Field, e.Reason)
}

func (e *ValidationError) Unwrap() error { return ErrInvalidDTO }

func invalid(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }

type SourceID struct {
	SessionID string `json:"session_id"`
	Sequence  uint64 `json:"sequence"`
}

func NewSourceID(sessionID string, sequence uint64) (SourceID, error) {
	id := SourceID{SessionID: sessionID, Sequence: sequence}
	return id, id.Validate()
}

func (id SourceID) Validate() error { return validateIdentifier("source_id.session_id", id.SessionID) }

type SourceRange struct {
	Start SourceID `json:"start"`
	End   SourceID `json:"end"`
}

func NewSourceRange(start, end SourceID) (SourceRange, error) {
	r := SourceRange{Start: start, End: end}
	return r, r.Validate()
}

func (r SourceRange) Validate() error {
	if err := r.Start.Validate(); err != nil {
		return err
	}
	if err := r.End.Validate(); err != nil {
		return err
	}
	if r.Start.SessionID != r.End.SessionID {
		return invalid("source_range", "start and end sessions differ")
	}
	if r.Start.Sequence > r.End.Sequence {
		return invalid("source_range", "start follows end")
	}
	if r.End.Sequence-r.Start.Sequence+1 > MaxSourceRangeEvents {
		return invalid("source_range", "range exceeds event limit")
	}
	return nil
}

type BindingRevision struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Generation uint64 `json:"generation"`
}

func NewBindingRevision(providerName, model string, generation uint64) (BindingRevision, error) {
	b := BindingRevision{Provider: providerName, Model: model, Generation: generation}
	return b, b.Validate()
}

func (b BindingRevision) Validate() error {
	if err := validateIdentifier("binding.provider", b.Provider); err != nil {
		return err
	}
	if err := validateIdentifier("binding.model", b.Model); err != nil {
		return err
	}
	if b.Generation == 0 {
		return invalid("binding.generation", "must be positive")
	}
	return nil
}

type Revision struct {
	Session uint64 `json:"session"`
	Durable uint64 `json:"durable"`
	Source  uint64 `json:"source"`
}

func NewRevision(session, durable, source uint64) Revision {
	return Revision{Session: session, Durable: durable, Source: source}
}

func (r Revision) Validate() error { return nil }

type CheckpointID struct {
	SessionID      string      `json:"session_id"`
	SourceRange    SourceRange `json:"source_range"`
	Algorithm      string      `json:"algorithm"`
	SchemaVersion  uint32      `json:"schema_version"`
	SummaryModel   string      `json:"summary_model"`
	IdempotencyKey string      `json:"idempotency_key"`
}

func NewCheckpointID(sessionID string, sourceRange SourceRange, algorithm string, schemaVersion uint32, summaryModel, key string) (CheckpointID, error) {
	id := CheckpointID{SessionID: sessionID, SourceRange: sourceRange, Algorithm: algorithm, SchemaVersion: schemaVersion, SummaryModel: summaryModel, IdempotencyKey: key}
	return id, id.Validate()
}

func (id CheckpointID) Validate() error {
	if err := validateIdentifier("checkpoint.session_id", id.SessionID); err != nil {
		return err
	}
	if err := id.SourceRange.Validate(); err != nil {
		return err
	}
	if id.SourceRange.Start.SessionID != id.SessionID {
		return invalid("checkpoint.source_range", "range belongs to another session")
	}
	if err := validateBoundedText("checkpoint.algorithm", id.Algorithm, 64, false); err != nil {
		return err
	}
	if id.SchemaVersion == 0 {
		return invalid("checkpoint.schema_version", "must be positive")
	}
	if err := validateBoundedText("checkpoint.summary_model", id.SummaryModel, MaxIdentifierBytes, true); err != nil {
		return err
	}
	return validateBoundedText("checkpoint.idempotency_key", id.IdempotencyKey, MaxIdentifierBytes, false)
}

type Principal struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
	SubjectID   string `json:"subject_id"`
	capability  [32]byte
}

func NewPrincipal(workspaceID, sessionID, subjectID string) (Principal, error) {
	p := Principal{WorkspaceID: workspaceID, SessionID: sessionID, SubjectID: subjectID}
	p.capability = sha256.Sum256([]byte(workspaceID + "\x00" + sessionID + "\x00" + subjectID))
	return p, p.Validate()
}

func (p Principal) IsBound() bool { return p.capability != [32]byte{} }

func (p Principal) Validate() error {
	for field, value := range map[string]string{"principal.workspace_id": p.WorkspaceID, "principal.session_id": p.SessionID, "principal.subject_id": p.SubjectID} {
		if err := validateIdentifier(field, value); err != nil {
			return err
		}
	}
	return nil
}

type ContentRef struct {
	Ref         string `json:"ref"`
	Namespace   string `json:"namespace"`
	SHA256      string `json:"sha256"`
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
	SubjectID   string `json:"subject_id"`
	Size        int    `json:"size"`
}

func (r ContentRef) Validate() error {
	if err := validateBoundedText("content.ref", r.Ref, MaxPayloadReferenceBytes, false); err != nil {
		return err
	}
	if r.Namespace != Namespace {
		return invalid("content.namespace", "unexpected namespace")
	}
	if len(r.SHA256) != 64 || !isLowerHex(r.SHA256) {
		return invalid("content.sha256", "must be a lowercase SHA-256 digest")
	}
	if err := validateIdentifier("content.workspace_id", r.WorkspaceID); err != nil {
		return err
	}
	if err := validateIdentifier("content.session_id", r.SessionID); err != nil {
		return err
	}
	if err := validateIdentifier("content.subject_id", r.SubjectID); err != nil {
		return err
	}
	if r.Size < 0 || r.Size > MaxSourceEventBytes {
		return invalid("content.size", "outside payload limit")
	}
	return nil
}

type RetentionClass string

const (
	RetentionSession    RetentionClass = "session"
	RetentionCompliance RetentionClass = "compliance"
)

type SanitizedPayload struct {
	Ref             ContentRef     `json:"ref"`
	Bytes           []byte         `json:"bytes,omitempty"`
	HashOnly        bool           `json:"hash_only"`
	Dereferenceable bool           `json:"dereferenceable"`
	Revoked         bool           `json:"revoked"`
	Retention       RetentionClass `json:"retention"`
}

type PayloadRecord struct {
	Ref       ContentRef     `json:"ref"`
	Retention RetentionClass `json:"retention"`
	Revoked   bool           `json:"revoked"`
	Data      []byte         `json:"data,omitempty"`
}

func (p PayloadRecord) Validate() error {
	if err := p.Ref.Validate(); err != nil {
		return err
	}
	if p.Retention == "" {
		return invalid("payload.retention", "must not be empty")
	}
	if p.Ref.Size != len(p.Data) && len(p.Data) > 0 {
		return invalid("payload.data", "size does not match content reference")
	}
	return nil
}

type SourceEvent struct {
	ID              SourceID `json:"id"`
	Kind            string   `json:"kind"`
	Role            string   `json:"role"`
	ToolCallID      string   `json:"tool_call_id,omitempty"`
	PayloadRef      string   `json:"payload_ref,omitempty"`
	Provenance      string   `json:"provenance"`
	RedactionStatus string   `json:"redaction_status"`
	Size            int      `json:"size"`
}

func (e SourceEvent) Validate() error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	for field, value := range map[string]string{"source.kind": e.Kind, "source.role": e.Role, "source.provenance": e.Provenance, "source.redaction_status": e.RedactionStatus} {
		if err := validateBoundedText(field, value, 256, false); err != nil {
			return err
		}
	}
	if e.PayloadRef != "" && len(e.PayloadRef) > MaxPayloadReferenceBytes {
		return invalid("source.payload_ref", "reference is too large")
	}
	if e.Size < 0 || e.Size > MaxSourceEventBytes {
		return invalid("source.size", "outside event payload limit")
	}
	return nil
}

type CheckpointRecord struct {
	ID              CheckpointID    `json:"id"`
	Revision        Revision        `json:"revision"`
	Binding         BindingRevision `json:"binding"`
	SourceRange     SourceRange     `json:"source_range"`
	ActiveContext   []byte          `json:"active_context"`
	SummaryMetadata []byte          `json:"summary_metadata"`
	TurnID          uint64          `json:"turn_id"`
	Complete        bool            `json:"complete"`
}

func (c CheckpointRecord) Validate() error {
	if err := c.ID.Validate(); err != nil {
		return err
	}
	if err := c.Binding.Validate(); err != nil {
		return err
	}
	if err := c.SourceRange.Validate(); err != nil {
		return err
	}
	if c.ID.SessionID != c.SourceRange.Start.SessionID || c.ID.SourceRange != c.SourceRange {
		return invalid("checkpoint.source_range", "does not match checkpoint identity")
	}
	if len(c.ActiveContext) == 0 || len(c.ActiveContext) > MaxCheckpointBytes {
		return invalid("checkpoint.active_context", "outside checkpoint limit")
	}
	if len(c.SummaryMetadata) > MaxCheckpointMetadata {
		return invalid("checkpoint.summary_metadata", "outside metadata limit")
	}
	if len(c.ActiveContext)+len(c.SummaryMetadata) > MaxCheckpointBytes {
		return invalid("checkpoint", "serialized checkpoint is too large")
	}
	if c.TurnID == 0 {
		return invalid("checkpoint.turn_id", "must be positive")
	}
	return nil
}

type Snapshot struct {
	Revision   Revision         `json:"revision"`
	Binding    BindingRevision  `json:"binding"`
	Active     CheckpointRecord `json:"active"`
	Source     []SourceEvent    `json:"source"`
	Tombstoned bool             `json:"tombstoned"`
}

type PolicySnapshot struct {
	SummaryEnabled      bool     `json:"summary_enabled"`
	RedactionConfigured bool     `json:"redaction_configured"`
	Provider            string   `json:"provider"`
	Model               string   `json:"model"`
	CredentialScope     string   `json:"credential_scope"`
	NetworkEnabled      bool     `json:"network_enabled"`
	EndpointAllowlist   []string `json:"endpoint_allowlist,omitempty"`
	PolicyDigest        string   `json:"policy_digest"`
}

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
}

type EnsureSessionRequest struct {
	Principal Principal       `json:"principal"`
	Binding   BindingRevision `json:"binding"`
}

func NewCommitRequest(principal Principal, sessionID string, expected Revision, expectedBinding BindingRevision, events []SourceEvent, checkpoint CheckpointRecord, activeContext []byte, newBinding BindingRevision, turnID uint64) (CommitRequest, error) {
	checkpoint.Complete = true
	digest := sha256.Sum256(activeContext)
	r := CommitRequest{OperationID: checkpoint.ID.IdempotencyKey, Principal: principal, SessionID: sessionID, Expected: expected, ExpectedBinding: expectedBinding, NewSourceEvents: events, Checkpoint: checkpoint, ActiveContext: activeContext, NewSession: expected.Session + 1, NewDurable: expected.Durable + 1, NewSourceSequence: expected.Source + uint64(len(events)), NewBinding: newBinding, TurnID: turnID, BaseDigest: hex.EncodeToString(digest[:])}
	return r, r.Validate()
}

func (r CommitRequest) Validate() error { return validateCommitRequest(r) }

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

func validateIdentifier(field, value string) error {
	return validateBoundedText(field, value, MaxIdentifierBytes, false)
}

func validateBoundedText(field, value string, max int, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return invalid(field, "must not be empty")
	}
	if len(value) > max || !utf8.ValidString(value) {
		return invalid(field, "invalid or too long")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return invalid(field, "contains a control character")
		}
	}
	return nil
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
