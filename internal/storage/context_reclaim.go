package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

var (
	_ contextstate.SessionReclaimer    = (*SQLite)(nil)
	_ contextstate.SessionLeaseRenewer = (*SQLite)(nil)
)

// sessionLeaseTTL is how long a context session's lease_at stays fresh after
// its last heartbeat renewal before ReclaimSession treats the row as
// abandoned and eligible for takeover. Mirrors the VALUE of
// internal/workflows/ledger.DefaultClaimLease (2 minutes): both are the same
// shape of problem - detect a dead holder without waiting so long that a
// crashed process blocks recovery, without being so short that a live
// holder's normal heartbeat cadence occasionally reads as stale. Local
// constant rather than importing the ledger package, which belongs to a
// different subsystem (workflow run claims, not context sessions) and would
// add a dependency this package does not otherwise need.
const sessionLeaseTTL = 2 * time.Minute

// ReclaimSession transfers write ownership of an existing, non-tombstoned
// live context session to principal's own freshly minted capability, then
// returns its current snapshot. It exists because Principal.capability is
// minted fresh and random per process and never persisted anywhere it could
// be recovered - a later process resuming a session by id (an id it learned
// through LoadSession/ListSessions, both scoped only to workspace+subject
// with no capability check) has no way to reconstruct the capability the
// original process held, so authorizing every other durable-write path on an
// exact capability match would make cross-process resume impossible by
// construction. Reclaiming is scoped the same way those reads already are:
// knowing the session's id, workspace and subject is what LoadSession and
// DeleteSessionSnapshot already treat as sufficient authority for the same
// session, so extending that authority to "take over its capability" adds no
// new trust boundary.
//
// A managed worktree session is rejected: those are addressed by name
// through the chat_sessions catalog (worktree_catalog_keys), never through
// this capability-gated context_sessions row, so reclaiming one here would
// be meaningless.
//
// The takeover deliberately does NOT stamp a fresh lease_at - only a real
// heartbeat tick (RenewLease) may mark a lease fresh. Stamping here (an
// earlier version did) meant every successful reclaim, even a totally
// uncontested one, poisoned the row against any other reclaim for the next
// sessionLeaseTTL - breaking one-shot commands (mivia compact, a quick chat
// -p turn) that never renew a lease at all. Tradeoff accepted instead: a
// THIRD process reclaiming within the sub-heartbeat-interval window right
// after this takeover can still succeed (benign churn, loud
// ErrPrincipalMismatch on the loser's next write) rather than the silent
// eviction this feature exists to prevent.
func (s *SQLite) ReclaimSession(ctx context.Context, principal contextstate.Principal, sessionID string) (contextstate.Snapshot, error) {
	if err := principal.Validate(); err != nil {
		return contextstate.Snapshot{}, err
	}
	if !principal.IsBound() || sessionID != principal.SessionID {
		return contextstate.Snapshot{}, contextstate.ErrPrincipalMismatch
	}
	now := time.Now()
	staleCutoff := now.Add(-sessionLeaseTTL).Unix()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return contextstate.Snapshot{}, err
	}
	var subjectID string
	var tombstoned int
	var instanceID sql.NullString
	var leaseAt sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT subject_id,tombstoned,instance_id,lease_at FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, sessionID).Scan(&subjectID, &tombstoned, &instanceID, &leaseAt)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, contextstate.ErrSessionNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, err
	}
	if subjectID != principal.SubjectID {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, contextstate.ErrPrincipalMismatch
	}
	if tombstoned != 0 {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, contextstate.ErrSessionTombstoned
	}
	if instanceID.Valid {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, fmt.Errorf("%w: managed worktree sessions cannot be reclaimed", contextstate.ErrInvalidDTO)
	}
	// beginWrite holds SQLite's write lock for the whole transaction, so the
	// SELECT above cannot be invalidated by another process before this
	// UPDATE lands: the lease state it just read is still the lease state the
	// UPDATE's predicate evaluates against. That is what makes the zero-rows
	// disambiguation below correct instead of racy. See the doc comment above
	// for why this UPDATE never writes lease_at.
	result, err := tx.ExecContext(ctx, `UPDATE context_sessions SET capability_digest=? WHERE workspace_id=? AND session_id=? AND subject_id=? AND tombstoned=0 AND instance_id IS NULL AND (lease_at IS NULL OR lease_at < ?)`, principal.CapabilityDigest(), principal.WorkspaceID, sessionID, principal.SubjectID, staleCutoff)
	if err != nil {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, err
	}
	if err := requireCatalogMutation(result); err != nil {
		_ = tx.Rollback()
		// The SELECT above already confirmed the session exists, is owned by
		// this subject, is not tombstoned, and is not a managed worktree
		// session - every other reason the UPDATE could affect zero rows.
		// The only remaining reason left is the lease predicate: it is still
		// fresh, so a live owner is rejecting the takeover instead of being
		// silently evicted mid-turn.
		if leaseAt.Valid && leaseAt.Int64 >= staleCutoff {
			// Typed refusal: the caller (and ultimately the user) needs the
			// holder's heartbeat age and the takeover horizon to tell a
			// short wait from a permanent failure.
			leaseTime := time.Unix(leaseAt.Int64, 0)
			return contextstate.Snapshot{}, &contextstate.SessionLiveError{
				LeaseAge:   now.Sub(leaseTime),
				RetryAfter: leaseTime.Add(sessionLeaseTTL).Sub(now),
			}
		}
		return contextstate.Snapshot{}, err
	}
	snapshot, err := loadContextTx(ctx, tx, principal, sessionID, contextstate.WorktreeInstance{})
	if err != nil {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, err
	}
	return snapshot, tx.Commit()
}

// RenewLease refreshes the caller's context session lease so ReclaimSession
// treats this process's ownership as live. Scoped by capability_digest, not
// just subject: a process whose capability was already reclaimed away by a
// second process cannot resurrect its own stale lease and block the new
// owner - the UPDATE simply matches zero rows for a capability that no
// longer owns the row, which RenewLease treats as a no-op rather than an
// error, since the caller has no standing to be told about a takeover it
// lost with no reclaim call of its own.
func (s *SQLite) RenewLease(ctx context.Context, principal contextstate.Principal, sessionID string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if !principal.IsBound() || sessionID != principal.SessionID {
		return contextstate.ErrPrincipalMismatch
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE context_sessions SET lease_at=? WHERE workspace_id=? AND session_id=? AND subject_id=? AND capability_digest=?`, time.Now().Unix(), principal.WorkspaceID, sessionID, principal.SubjectID, principal.CapabilityDigest())
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ReleaseLease clears the caller's lease on a clean shutdown, so the next
// resume of this session id sees an immediately-stale (NULL) lease instead
// of waiting out sessionLeaseTTL against a process that already quit.
// Scoped by capability_digest exactly like RenewLease: a process whose
// capability was already reclaimed away matches zero rows and this is a
// no-op, not an error - it has nothing left to release.
func (s *SQLite) ReleaseLease(ctx context.Context, principal contextstate.Principal, sessionID string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if !principal.IsBound() || sessionID != principal.SessionID {
		return contextstate.ErrPrincipalMismatch
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE context_sessions SET lease_at=NULL WHERE workspace_id=? AND session_id=? AND subject_id=? AND capability_digest=?`, principal.WorkspaceID, sessionID, principal.SubjectID, principal.CapabilityDigest())
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
