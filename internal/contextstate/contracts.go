// Package contextstate is the CLI's durable context contract layer. It
// composes the upstream mivia-ai-sdk/contextstate transport structs with
// the CLI's identity, redaction, worktree, and lifecycle extensions.
//
// The byte-identical subset (shape constants and the ref-mint
// primitives) is re-exported from mivia-ai-sdk/contextstate so every
// CLI caller sees the SDK's canonical implementation under one symbol.
// The divergent subset (ContentRef with the CLI's "ctxp_<hex>"
// primary-key namespace, CheckpointID with the CLI's SummaryModel
// field, the commit/checkpoint records with the CLI's Principal,
// fingerprint, and worktree-instance fields, and the 13 CLI sentinel
// errors) stays as distinct local types so the CLI's failure modes and
// persisted shape do not move with an SDK release.
package contextstate

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SourceID names one event: a session and a sequence number. The local
// Validate wraps ErrInvalidDTO so the CLI's failure mode does not move
// with an SDK release; the SDK's Validate wraps ErrInvalidRecord.
type SourceID struct {
	SessionID string `json:"session_id"`
	Sequence  uint64 `json:"sequence"`
}

// NewSourceID builds one SourceID and validates it.
func NewSourceID(sessionID string, sequence uint64) (SourceID, error) {
	id := SourceID{SessionID: sessionID, Sequence: sequence}
	return id, id.Validate()
}

// Validate bounds the identifier.
func (id SourceID) Validate() error { return validateIdentifier("source_id.session_id", id.SessionID) }

// SourceRange spans events of one session, inclusive of both ends.
type SourceRange struct {
	Start SourceID `json:"start"`
	End   SourceID `json:"end"`
}

// NewSourceRange builds one SourceRange and validates it.
func NewSourceRange(start, end SourceID) (SourceRange, error) {
	r := SourceRange{Start: start, End: end}
	return r, r.Validate()
}

// Validate enforces one session, an ordered span, and a span under
// MaxSourceRangeEvents.
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
	if r.End.Sequence-r.Start.Sequence >= MaxSourceRangeEvents {
		return invalid("source_range", "range exceeds event limit")
	}
	return nil
}

// BindingRevision names the provider-model pair and its generation.
type BindingRevision struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Generation uint64 `json:"generation"`
}

// NewBindingRevision builds one BindingRevision and validates it.
func NewBindingRevision(providerName, model string, generation uint64) (BindingRevision, error) {
	b := BindingRevision{Provider: providerName, Model: model, Generation: generation}
	return b, b.Validate()
}

// Validate bounds both identifiers and requires a positive generation.
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

// Revision is a session's three counters. It carries no Validate; the
// commit rules compare it as a whole.
type Revision struct {
	Session uint64 `json:"session"`
	Durable uint64 `json:"durable"`
	Source  uint64 `json:"source"`
}

// NewRevision builds one Revision from its three counters.
func NewRevision(session, durable, source uint64) Revision {
	return Revision{Session: session, Durable: durable, Source: source}
}

// Validate always returns nil so Revision stays a value type callers
// can range over.
func (r Revision) Validate() error { return nil }

// CheckpointID identifies one checkpoint within a session. The CLI
// adds SummaryModel so storage can record which model authored the
// summary; the SDK has no such field, so this type stays local.
type CheckpointID struct {
	SessionID      string      `json:"session_id"`
	SourceRange    SourceRange `json:"source_range"`
	Algorithm      string      `json:"algorithm"`
	SchemaVersion  uint32      `json:"schema_version"`
	SummaryModel   string      `json:"summary_model"`
	IdempotencyKey string      `json:"idempotency_key"`
}

// NewCheckpointID builds one CheckpointID and validates it.
func NewCheckpointID(sessionID string, sourceRange SourceRange, algorithm string, schemaVersion uint32, summaryModel, key string) (CheckpointID, error) {
	id := CheckpointID{SessionID: sessionID, SourceRange: sourceRange, Algorithm: algorithm, SchemaVersion: schemaVersion, SummaryModel: summaryModel, IdempotencyKey: key}
	return id, id.Validate()
}

// Validate enforces the identifier bounds, a valid same-session
// SourceRange, an Algorithm bounded at 64 bytes, a positive
// SchemaVersion, an optional bounded SummaryModel, and a bounded
// IdempotencyKey.
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

// Principal is the CLI's three-tuple identity plus a per-process
// capability secret. The SDK has no equivalent; storage persists the
// capability digest alongside the owner tuple so a second handle with
// matching strings cannot authorize reads or writes.
type Principal struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
	SubjectID   string `json:"subject_id"`
	capability  [32]byte
}

// NewPrincipal mints a Principal with a fresh capability secret.
func NewPrincipal(workspaceID, sessionID, subjectID string) (Principal, error) {
	p := Principal{WorkspaceID: workspaceID, SessionID: sessionID, SubjectID: subjectID}
	if _, err := rand.Read(p.capability[:]); err != nil {
		return Principal{}, fmt.Errorf("mint principal capability: %w", err)
	}
	return p, p.Validate()
}

// IsBound reports whether the Principal carries a non-zero capability.
func (p Principal) IsBound() bool { return p.capability != [32]byte{} }

// CapabilityDigest is the durable, non-reversible identity of this
// principal handle. Storage persists this digest alongside the owner
// tuple so a second handle with matching strings cannot authorize
// reads or writes.
func (p Principal) CapabilityDigest() string {
	if !p.IsBound() {
		return ""
	}
	digest := sha256.Sum256(p.capability[:])
	return hex.EncodeToString(digest[:])
}

// Validate bounds the three owner strings at MaxIdentifierBytes.
func (p Principal) Validate() error {
	for field, value := range map[string]string{"principal.workspace_id": p.WorkspaceID, "principal.session_id": p.SessionID, "principal.subject_id": p.SubjectID} {
		if err := validateIdentifier(field, value); err != nil {
			return err
		}
	}
	return nil
}

// ContentRef is the CLI's durable address of one shared context blob.
// The SDK's ContentRef enforces Ref == HashPrefix+SHA256; the CLI uses
// "ctxp_<hex>" refs derived from the owner tuple so two principals
// with identical content do not collide on a global primary key. The
// two types share their field set and stay as two distinct Go types so
// the local validator can enforce the CLI's namespace.
type ContentRef struct {
	Ref         string `json:"ref"`
	Namespace   string `json:"namespace"`
	SHA256      string `json:"sha256"`
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
	SubjectID   string `json:"subject_id"`
	Size        int    `json:"size"`
}

// Validate bounds the Ref, enforces the CLI namespace, requires a
// lowercase 64-character SHA-256, bounds the owner strings, and
// rejects a negative Size. The validator refuses any "sha256:"-prefixed
// Ref so an SDK reference cannot pass through the CLI's storage.
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

// RetentionClass labels how long a payload is kept.
type RetentionClass string

const (
	// RetentionSession keeps a payload for the session's lifetime.
	RetentionSession RetentionClass = "session"
	// RetentionCompliance keeps a payload past session deletion.
	RetentionCompliance RetentionClass = "compliance"
)

// SanitizedPayload is the CLI's content-addressed payload shape with
// the redaction outcome attached.
type SanitizedPayload struct {
	Ref             ContentRef     `json:"ref"`
	Bytes           []byte         `json:"bytes,omitempty"`
	HashOnly        bool           `json:"hash_only"`
	Dereferenceable bool           `json:"dereferenceable"`
	Revoked         bool           `json:"revoked"`
	Retention       RetentionClass `json:"retention"`
}

// PayloadRecord is one stored payload under its content address. The
// field set matches the SDK's, but Ref is the CLI's local ContentRef
// (sha256:-prefixed SDK references do not pass Validate), so the type
// stays local.
type PayloadRecord struct {
	Ref       ContentRef     `json:"ref"`
	Retention RetentionClass `json:"retention"`
	Revoked   bool           `json:"revoked"`
	Data      []byte         `json:"data,omitempty"`
}

// Validate enforces a valid Ref, a non-empty Retention, and, when
// Data is present, a length and digest that match Ref.
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

// SourceEvent is one durable event in a session's source log. The
// field set matches the SDK's; the local Validate wraps ErrInvalidDTO
// so the CLI's failure mode does not move with an SDK release.
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

// Validate bounds the four required text fields at 256 bytes, the two
// optional fields when set, and rejects a negative Size.
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

// CheckpointRecord is one committed state of a session with the CLI's
// summary metadata column and Complete flag. Distinct from the SDK's
// Checkpoint because storage persists Complete and SummaryMetadata.
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

// Validate enforces a valid ID, a valid Binding, a SourceRange that
// matches the ID, a non-empty ActiveContext within the CheckpointBytes
// bound, a SummaryMetadata within the EffectiveCheckpointMetadataLimit,
// a positive TurnID, and that ActiveContext + SummaryMetadata fit the
// overall checkpoint bound.
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

// Snapshot is the read model of one session. Tombstoned marks a
// session that DeleteSession removed.
type Snapshot struct {
	Revision   Revision         `json:"revision"`
	Binding    BindingRevision  `json:"binding"`
	Active     CheckpointRecord `json:"active"`
	Source     []SourceEvent    `json:"source"`
	Tombstoned bool             `json:"tombstoned"`
}

// PolicySnapshot exposes the resolved redaction and credential policy
// to the runtime so a UI surface can render the operator's ceiling.
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

// validateIdentifier bounds a required identifier at MaxIdentifierBytes.
func validateIdentifier(field, value string) error {
	return validateBoundedText(field, value, MaxIdentifierBytes, false)
}

// validateBoundedText rejects empty text when required, then text
// over max, invalid UTF-8, or a control character.
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

// isLowerHex reports whether every character in value is a lowercase hex digit.
// An empty string passes (length is checked separately at the caller).
func isLowerHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
