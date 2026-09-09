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
	WorktreeInstance  WorktreeInstance `json:"worktree_instance,omitempty"`
}

type EnsureSessionRequest struct {
	Principal Principal       `json:"principal"`
	Binding   BindingRevision `json:"binding"`
	// Dir and Worktree record where the live session lives. They are written
	// once with the session row and drive TUI session restore.
	Dir      string `json:"dir,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	// WorktreeInstance binds this session to one physical managed worktree.
	WorktreeInstance WorktreeInstance `json:"worktree_instance,omitempty"`
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
	OperationID        string           `json:"operation_id"`
	Principal          Principal        `json:"principal"`
	SessionID          string           `json:"session_id"`
	Expected           Revision         `json:"expected"`
	ExpectedBinding    BindingRevision  `json:"expected_binding"`
	NewSession         uint64           `json:"new_session"`
	NewDurable         uint64           `json:"new_durable"`
	NewSourceSequence  uint64           `json:"new_source_sequence"`
	NewBinding         BindingRevision  `json:"new_binding"`
	ActiveCheckpointID string           `json:"active_checkpoint_id,omitempty"`
	ClearActive        bool             `json:"clear_active"`
	Reason             string           `json:"reason"`
	WorktreeInstance   WorktreeInstance `json:"worktree_instance,omitempty"`
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
	if err := r.WorktreeInstance.Validate(); err != nil {
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

// WorktreeStore is the optional scoped read surface for a managed worktree.
// Implementations reject a non-active or mismatched physical instance.
type WorktreeStore interface {
	LoadWorktree(context.Context, Principal, string, WorktreeInstance) (Snapshot, error)
}

// SessionReclaimer is the optional surface that lets a resumed process take
// over write ownership of an existing, non-tombstoned live context session.
// A session's owner capability is minted fresh and only ever held in the
// process that created it (Principal.capability), so a later process that
// legitimately knows the session's id - the same id LoadSession/
// DeleteSessionSnapshot already accept scoped only to workspace+subject,
// with no capability check - is trusted to reclaim it for resumed commits.
// Without this, a resumed session can read its prior history but every
// subsequent turn commits under the resuming process's own, unrelated
// session id instead of updating the one the caller asked to resume.
type SessionReclaimer interface {
	ReclaimSession(context.Context, Principal, string) (Snapshot, error)
}

// WorktreeSessionReclaimer is SessionReclaimer for a session bound to a
// managed worktree instance. ReclaimSession refuses instance-bound rows on
// purpose (an unbound reader must never take over a worktree session); a
// resuming process that has re-bound the SAME instance first (StartInRoute
// before Load) is the legitimate owner and reclaims through this surface.
// Without it a resumed worktree session never reclaimed at all and its next
// turn silently forked into a second context session.
type WorktreeSessionReclaimer interface {
	ReclaimWorktreeSession(context.Context, Principal, string, WorktreeInstance) (Snapshot, error)
}

// SessionLeaseRenewer is the optional surface a live process uses to prove to
// ReclaimSession that it is still actively working a session, so a second
// process resuming the same session id cannot silently evict it mid-turn.
// RenewLease is scoped by capability_digest (not just subject) so a process
// whose capability was already reclaimed away cannot resurrect its own stale
// lease and block the process that legitimately took over.
//
// ReleaseLease clears a lease this process is voluntarily giving up (a clean
// shutdown), so the NEXT resume of this same session id does not have to
// wait out the staleness TTL just because this process quit before its
// lease happened to expire on its own - without this, a heartbeat that had
// renewed even once looks "live" to ReclaimSession for the full TTL after
// the owning process is already gone, and an ordinary "quit, then resume"
// within that window is refused as ErrSessionLiveElsewhere even though
// nothing is actually still using the session.
type SessionLeaseRenewer interface {
	RenewLease(ctx context.Context, principal Principal, sessionID string) error
	ReleaseLease(ctx context.Context, principal Principal, sessionID string) error
}

type SourceReader interface {
	ReadRange(context.Context, Principal, SourceRange) ([]SourceEvent, error)
	ReadPayload(context.Context, Principal, ContentRef) (SanitizedPayload, error)
}
