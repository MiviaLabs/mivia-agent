// Package contextstate defines the dependency-neutral durable context
// contract. It deliberately knows nothing about providers, chat, or storage.
package contextstate

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Shape bounds. These describe the FORM a durable value must take rather than
// how much the user said, so they stay compiled in: an identifier that long is
// malformed at any scale, and a source range that wide breaks range arithmetic
// rather than merely costing storage. Volume bounds live in Limits, are
// operator-owned, and are uncapped by default - see limits.go.
const (
	Namespace                    = "mivia.context.payload.v1"
	MaxIdentifierBytes           = 128
	MaxPayloadReferenceBytes     = 256
	MaxSourceRangeEvents         = 100_000
	DefaultMaxCheckpointMetadata = 16 * 1024
	DefaultMaxSummaryMetadata    = 12 * 1024
	MaxAuditBytes                = 1 * 1024
)

var (
	ErrInvalidDTO           = errors.New("invalid context DTO")
	ErrPrincipalMismatch    = errors.New("principal mismatch")
	ErrSessionNotFound      = errors.New("session not found")
	ErrSessionTombstoned    = errors.New("session tombstoned")
	ErrPayloadNotFound      = errors.New("payload not found")
	ErrPayloadRevoked       = errors.New("payload revoked")
	ErrExportTooLarge       = errors.New("context export too large")
	ErrSummaryUnavailable   = errors.New("summary unavailable")
	ErrStaleRevision        = errors.New("stale revision")
	ErrStaleBinding         = errors.New("stale binding")
	ErrCheckpointConflict   = errors.New("checkpoint conflict")
	ErrWorktreeDeleted      = errors.New("worktree session deleted")
	ErrPromptBudgetExceeded = errors.New("prompt budget exceeded")
)

// EffectiveSummaryMetadataLimit returns the operator-configured summary
// metadata bound, falling back to the compiled-in default when uncapped.
func EffectiveSummaryMetadataLimit() int {
	if v := CurrentLimits().SummaryMetadataBytes; v > 0 {
		return v
	}
	return DefaultMaxSummaryMetadata
}

// EffectiveCheckpointMetadataLimit returns the operator-configured
// checkpoint metadata bound, falling back to the compiled-in default.
func EffectiveCheckpointMetadataLimit() int {
	if v := CurrentLimits().CheckpointMetadataBytes; v > 0 {
		return v
	}
	return DefaultMaxCheckpointMetadata
}

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
	if _, err := rand.Read(p.capability[:]); err != nil {
		return Principal{}, fmt.Errorf("mint principal capability: %w", err)
	}
	return p, p.Validate()
}

func (p Principal) IsBound() bool { return p.capability != [32]byte{} }

// CapabilityDigest is the durable, non-reversible identity of this principal
// handle. Storage persists this digest alongside the owner tuple so a second
// handle with matching strings cannot authorize reads or writes.
func (p Principal) CapabilityDigest() string {
	if !p.IsBound() {
		return ""
	}
	digest := sha256.Sum256(p.capability[:])
	return hex.EncodeToString(digest[:])
}

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
	// Whole-payload size is uncapped (chunked at storage). Only reject negative.
	if r.Size < 0 {
		return invalid("content.size", "must not be negative")
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
	if len(p.Data) > 0 && p.Ref.Size != len(p.Data) {
		return invalid("payload.data", "size does not match content reference")
	}
	if len(p.Data) > 0 {
		digest := sha256.Sum256(p.Data)
		if hex.EncodeToString(digest[:]) != p.Ref.SHA256 {
			return invalid("payload.data", "does not match content reference digest")
		}
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
	if e.ToolCallID != "" {
		if err := validateBoundedText("source.tool_call_id", e.ToolCallID, MaxIdentifierBytes, false); err != nil {
			return err
		}
	}
	if e.PayloadRef != "" {
		if err := validateBoundedText("source.payload_ref", e.PayloadRef, MaxPayloadReferenceBytes, false); err != nil {
			return err
		}
	}
	// Whole-event payload size is uncapped; storage chunks under PayloadChunkSize.
	if e.Size < 0 {
		return invalid("source.size", "must not be negative")
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
	checkpointBytes := CurrentLimits().CheckpointBytes
	if len(c.ActiveContext) == 0 || exceedsLimit(len(c.ActiveContext), checkpointBytes) {
		return invalid("checkpoint.active_context", "outside checkpoint limit")
	}
	if len(c.SummaryMetadata) > EffectiveCheckpointMetadataLimit() {
		return invalid("checkpoint.summary_metadata", "outside metadata limit")
	}
	if exceedsLimit(len(c.ActiveContext)+len(c.SummaryMetadata), checkpointBytes) {
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
