package contextstate

import (
	"errors"
	"fmt"

	sdkctx "github.com/MiviaLabs/mivia-ai-sdk/contextstate"
)

// Shape bounds the SDK pins FOR the transport structs, re-exported so
// callers read "contextstate.MaxIdentifierBytes" rather than reaching
// into the SDK directly. Volume bounds live in the local Limits type
// (limits.go); they are operator-owned and uncapped by default.
const (
	// MaxIdentifierBytes is the byte cap on every transport identifier.
	MaxIdentifierBytes = sdkctx.MaxIdentifierBytes
	// MaxPayloadReferenceBytes is the byte cap on a payload reference.
	MaxPayloadReferenceBytes = sdkctx.MaxPayloadReferenceBytes
	// MaxSourceRangeEvents is the inclusive span that keeps range arithmetic honest.
	MaxSourceRangeEvents = sdkctx.MaxSourceRangeEvents
	// HashPrefix is the SDK's canonical content-address prefix.
	HashPrefix = sdkctx.HashPrefix

	// Namespace is the CLI's local payload namespace. The SDK exposes
	// none, and the CLI's ContentRef rejects every other value at
	// Validate time, so this stays local.
	Namespace = "mivia.context.payload.v1"

	// DefaultMaxCheckpointMetadata bounds a checkpoint's summary_metadata column.
	DefaultMaxCheckpointMetadata = 16 * 1024
	// DefaultMaxSummaryMetadata bounds the persisted summary envelope.
	DefaultMaxSummaryMetadata = 12 * 1024
	// MaxAuditBytes bounds an audit record's serialized size.
	MaxAuditBytes = 1 * 1024
)

// CLI sentinels. The names intentionally overlap with the SDK's
// sentinels (ErrSessionNotFound, ErrStaleRevision, ...); callers write
// errors.Is(err, contextstate.ErrSessionNotFound) and resolve to the
// CLI's, not the SDK's, so storage code that wraps %w produces a
// caller-visible chain that points back at this package.
var (
	// ErrInvalidDTO wraps every CLI DTO validation failure.
	ErrInvalidDTO = errors.New("invalid context DTO")
	// ErrPrincipalMismatch marks a payload whose owner tuple does not match the writing principal.
	ErrPrincipalMismatch = errors.New("principal mismatch")
	// ErrSessionNotFound marks a read of an unknown session.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionTombstoned marks a read of a session that has been deleted.
	ErrSessionTombstoned = errors.New("session tombstoned")
	// ErrPayloadNotFound marks a read of an unknown payload.
	ErrPayloadNotFound = errors.New("payload not found")
	// ErrPayloadRevoked marks a Get of a payload marked revoked.
	ErrPayloadRevoked = errors.New("payload revoked")
	// ErrExportTooLarge marks a context export that broke the ExportBytes bound.
	ErrExportTooLarge = errors.New("context export too large")
	// ErrSummaryUnavailable marks a read of a summary that has not yet been built.
	ErrSummaryUnavailable = errors.New("summary unavailable")
	// ErrStaleRevision marks a commit against a moved revision.
	ErrStaleRevision = errors.New("stale revision")
	// ErrStaleBinding marks a commit against a moved binding.
	ErrStaleBinding = errors.New("stale binding")
	// ErrCheckpointConflict marks a reused operation key carrying a different request.
	ErrCheckpointConflict = errors.New("checkpoint conflict")
	// ErrWorktreeDeleted marks a commit or read against a deleted worktree instance.
	ErrWorktreeDeleted = errors.New("worktree session deleted")
	// ErrPromptBudgetExceeded marks a prompt that broke the model's input budget.
	ErrPromptBudgetExceeded = errors.New("prompt budget exceeded")
	// ErrSessionLiveElsewhere marks a ReclaimSession attempt against a session
	// whose current owner's lease is still fresh (an actively-heartbeating
	// process), rather than the session being unknown, tombstoned, or owned by
	// a different subject/managed worktree.
	ErrSessionLiveElsewhere = errors.New("context session is live in another process")
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

// Error renders the sentinel, the field, and the reason.
func (e *ValidationError) Error() string {
	if e == nil {
		return ErrInvalidDTO.Error()
	}
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", ErrInvalidDTO, e.Reason)
	}
	return fmt.Sprintf("%s: %s: %s", ErrInvalidDTO, e.Field, e.Reason)
}

// Unwrap reports the sentinel under every validation failure.
func (e *ValidationError) Unwrap() error { return ErrInvalidDTO }

// invalid builds the wrapped validation failure for one field.
func invalid(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }

// Digest returns the SDK's SHA-256 of the ordered concatenation of chunks,
// as 64 lowercase hex characters. The CLI never mixes namespace or owner
// fields into the digest.
func Digest(chunks ...[]byte) string { return sdkctx.Digest(chunks...) }

// Mint returns the SDK's canonical content address of the concatenated
// chunks: HashPrefix plus Digest. The CLI's own contentRefID is a different
// function with a different prefix.
func Mint(chunks ...[]byte) string { return sdkctx.Mint(chunks...) }

// IsRef reports whether ref matches the SDK's canonical "sha256:<64 hex>"
// shape. The CLI's own contentRefID mints "ctxp_<hex>" strings that fail
// this check, which is the point: a CLI reference is not an SDK reference.
func IsRef(s string) bool { return sdkctx.IsRef(s) }

// NewContentRef mints an SDK-shaped ContentRef. The CLI's own
// contentRefID minter lives in sanitize.go.
func NewContentRef(namespace, workspaceID, sessionID, subjectID string, chunks ...[]byte) (sdkctx.ContentRef, error) {
	return sdkctx.NewContentRef(namespace, workspaceID, sessionID, subjectID, chunks...)
}
