package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
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
// live context session to principal's fresh capability, then returns its snapshot.
//
// Principal.capability is random per process and never persisted. Resuming
// processes find sessions by id through LoadSession or ListSessions (scoped to
// workspace and subject). Requiring the original capability would prevent
// cross-process resume. Authorization matches LoadSession and DeleteSessionSnapshot:
// workspace, subject, and session id grant authority to take over the capability.
// Managed worktree sessions are rejected because they use worktree_catalog_keys.
//
// Takeover does not update lease_at; only RenewLease marks leases fresh. Stamping
// lease_at on reclaim would block subsequent reclaims for sessionLeaseTTL, breaking
// one-shot commands (such as compact or single-turn chat) that never renew leases.
// A third process reclaiming within the sub-heartbeat window can still succeed,
// returning ErrPrincipalMismatch on the loser's next write instead of silent eviction.
func (s *SQLite) ReclaimSession(ctx context.Context, principal contextstate.Principal, sessionID string) (contextstate.Snapshot, error) {
	return s.reclaimSession(ctx, principal, sessionID, contextstate.WorktreeInstance{})
}

// ReclaimWorktreeSession is ReclaimSession for a row bound to instance: the
// caller has already re-bound that instance (StartInRoute before Load), so it
// is the legitimate owner. Every guard ReclaimSession applies still applies;
// the only difference is which namespace the row must live in. An instance
// mismatch (or a plain row) is refused exactly as ReclaimSession refuses a
// bound one.
func (s *SQLite) ReclaimWorktreeSession(ctx context.Context, principal contextstate.Principal, sessionID string, instance contextstate.WorktreeInstance) (contextstate.Snapshot, error) {
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return contextstate.Snapshot{}, fmt.Errorf("%w: invalid worktree instance", contextstate.ErrInvalidDTO)
	}
	return s.reclaimSession(ctx, principal, sessionID, instance)
}

func (s *SQLite) reclaimSession(ctx context.Context, principal contextstate.Principal, sessionID string, instance contextstate.WorktreeInstance) (contextstate.Snapshot, error) {
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
	if !instance.IsZero() {
		if err := requireActiveWorktreeTx(ctx, tx, principal, instance); err != nil {
			_ = tx.Rollback()
			return contextstate.Snapshot{}, err
		}
	}
	leaseAt, leaseHolder, err := reclaimRowState(ctx, tx, principal, sessionID, instance)
	if err != nil {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, err
	}
	// A provably dead holder's fresh lease is treated as stale: the lease
	// fence exists to protect a LIVE owner from silent eviction, and proof
	// of death (same host and boot, pid gone or reused - lease_liveness.go)
	// removes the owner it protects. Raising the cutoff makes the UPDATE's
	// lease predicate pass, so a crashed process blocks resume for zero
	// seconds instead of the rest of sessionLeaseTTL. Anything short of
	// proof leaves the cutoff alone and keeps the pure-TTL wait.
	if leaseHolder.Valid && leaseHolderDead(leaseHolder.String) {
		staleCutoff = math.MaxInt64
	}
	// beginWrite holds SQLite's write lock for the whole transaction, so the
	// SELECT above cannot be invalidated by another process before this
	// UPDATE lands: the lease state it just read is still the lease state the
	// UPDATE's predicate evaluates against. That is what makes the zero-rows
	// disambiguation below correct instead of racy. See the doc comment above
	// for why this UPDATE never writes lease_at.
	result, err := tx.ExecContext(ctx, `UPDATE context_sessions SET capability_digest=? WHERE workspace_id=? AND session_id=? AND subject_id=? AND tombstoned=0 AND instance_id IS ? AND (lease_at IS NULL OR lease_at < ?)`, principal.CapabilityDigest(), principal.WorkspaceID, sessionID, principal.SubjectID, nullableText(instance.ID), staleCutoff)
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
	snapshot, err := loadContextTx(ctx, tx, principal, sessionID, instance)
	if err != nil {
		_ = tx.Rollback()
		return contextstate.Snapshot{}, err
	}
	return snapshot, tx.Commit()
}

// reclaimRowState reads the session row and enforces every non-lease guard:
// existence, subject ownership, tombstone, and the managed-worktree
// rejection. It returns the row's lease state for the caller's takeover
// decision; the caller owns the transaction and rolls back on any error.
func reclaimRowState(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, sessionID string, instance contextstate.WorktreeInstance) (sql.NullInt64, sql.NullString, error) {
	var subjectID string
	var tombstoned int
	var instanceID sql.NullString
	var leaseAt sql.NullInt64
	var leaseHolder sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT subject_id,tombstoned,instance_id,lease_at,lease_holder FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, sessionID).Scan(&subjectID, &tombstoned, &instanceID, &leaseAt, &leaseHolder)
	if errors.Is(err, sql.ErrNoRows) {
		return leaseAt, leaseHolder, contextstate.ErrSessionNotFound
	}
	if err != nil {
		return leaseAt, leaseHolder, err
	}
	if subjectID != principal.SubjectID {
		return leaseAt, leaseHolder, contextstate.ErrPrincipalMismatch
	}
	if tombstoned != 0 {
		return leaseAt, leaseHolder, contextstate.ErrSessionTombstoned
	}
	if instance.IsZero() && instanceID.Valid {
		return leaseAt, leaseHolder, fmt.Errorf("%w: managed worktree sessions cannot be reclaimed", contextstate.ErrInvalidDTO)
	}
	if !instance.IsZero() && (!instanceID.Valid || instanceID.String != instance.ID) {
		return leaseAt, leaseHolder, contextstate.ErrWorktreeDeleted
	}
	return leaseAt, leaseHolder, nil
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
	_, err = tx.ExecContext(ctx, `UPDATE context_sessions SET lease_at=?, lease_holder=? WHERE workspace_id=? AND session_id=? AND subject_id=? AND capability_digest=?`, time.Now().Unix(), currentLeaseHolder(), principal.WorkspaceID, sessionID, principal.SubjectID, principal.CapabilityDigest())
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
	_, err = tx.ExecContext(ctx, `UPDATE context_sessions SET lease_at=NULL, lease_holder=NULL WHERE workspace_id=? AND session_id=? AND subject_id=? AND capability_digest=?`, principal.WorkspaceID, sessionID, principal.SubjectID, principal.CapabilityDigest())
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
