package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// deleteSessionAdmissionSQL reclaims a named session's admission record.
// chat_session_admissions carries no foreign key to the catalog, so every
// delete path has to name it explicitly or the row - and the agent and tool
// names in it - outlives the session forever.
const deleteSessionAdmissionSQL = `DELETE FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id IS NULL`

// deleteSessionDirSQL reclaims a named session's directory record. It is the
// same shape as the admission record: a side table with no foreign key, so
// every delete path names it explicitly or the row outlives the session.
const deleteSessionDirSQL = `DELETE FROM chat_session_dirs WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id IS NULL`

func (s *SQLite) DeleteSessionSnapshot(ctx context.Context, principal contextstate.Principal, name string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := validateSessionCatalogName(name); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	count, liveID, err := s.deleteSessionSnapshotRow(ctx, principal, name)
	if err != nil {
		return err
	}
	if count == 0 {
		// The name-keyed lookup found nothing, but a caller may be passing a
		// snapshot's stored session_id rather than its catalog name (e.g.
		// mivia-agent-desktop's AgentSessionSummary.session_id, which is
		// backfilled from other columns and is not guaranteed to equal the
		// row's `name` for legacy/diverged data - see
		// TestSQLiteChatSessionCatalogDeleteFindsSnapshotByStoredSessionID).
		// Resolve name from session_id before falling through to the
		// context-session tombstone path, which only matches
		// context_sessions.session_id and would otherwise silently no-op,
		// leaving the snapshot (and its chat_session_dirs project
		// association) orphaned forever.
		if resolvedName, ok, err := s.resolveSnapshotNameBySessionID(ctx, principal, name); err != nil {
			return err
		} else if ok {
			count, liveID, err = s.deleteSessionSnapshotRow(ctx, principal, resolvedName)
			if err != nil {
				return err
			}
		}
	}
	if count == 0 {
		return s.deleteContextSessionOrOrphanedAdmission(ctx, principal, name)
	}
	if liveID != "" {
		// The snapshot was a projection of a live session ("id is id"), and
		// LoadSession serves that live row whenever no snapshot remains - so
		// deleting the snapshot alone reported success and kept serving the
		// conversation. Retire the live row through the same lifecycle the
		// no-snapshot path uses, keyed by the row's STORED id (which diverges
		// from the catalog name in legacy data). A snapshot with no live row
		// behind it is already fully deleted.
		if err := s.deleteCatalogContextSession(ctx, principal, liveID); err != nil && !errors.Is(err, contextstate.ErrSessionNotFound) {
			return err
		}
	}
	return nil
}

// resolveSnapshotNameBySessionID looks up a chat_sessions snapshot's catalog
// name from its stored session_id column, for callers that identify a
// session by session_id rather than by name. See DeleteSessionSnapshot.
func (s *SQLite) resolveSnapshotNameBySessionID(ctx context.Context, principal contextstate.Principal, sessionID string) (string, bool, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND session_id=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, sessionID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}

// deleteContextSessionOrOrphanedAdmission handles the case the snapshot delete
// matched nothing. Either the name is context-backed - and the retention
// transaction owns reclaiming its admission record atomically with the
// tombstone - or nothing owns the name at all, in which case any admission row
// left behind is an orphan that no other path will ever reclaim.
func (s *SQLite) deleteContextSessionOrOrphanedAdmission(ctx context.Context, principal contextstate.Principal, name string) error {
	err := s.deleteCatalogContextSession(ctx, principal, name)
	if !errors.Is(err, contextstate.ErrSessionNotFound) {
		return err
	}
	if _, sweepErr := s.db.ExecContext(ctx, deleteSessionAdmissionSQL, principal.WorkspaceID, principal.SubjectID, name); sweepErr != nil {
		return sweepErr
	}
	if _, sweepErr := s.db.ExecContext(ctx, deleteSessionDirSQL, principal.WorkspaceID, principal.SubjectID, name); sweepErr != nil {
		return sweepErr
	}
	return err
}

// deleteSessionSnapshotRow removes a snapshot and its admission record in one
// transaction and reports how many snapshot rows it removed, so the caller can
// fall back to the context-backed delete path.
//
// The admission record is only reclaimed when the snapshot delete actually
// matched. A name this transaction did not own may still be owned by a
// context-backed session, and that session's retention transaction is a
// separate commit: reclaiming the record here would durably destroy it before
// the retention work is known to have landed, leaving a live session with no
// admitted tool set.
func (s *SQLite) deleteSessionSnapshotRow(ctx context.Context, principal contextstate.Principal, name string) (int64, string, error) {
	var count int64
	var liveID string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := rejectManagedCatalogKey(ctx, tx, principal, name); err != nil {
			return err
		}
		var storedSessionID sql.NullString
		switch err := tx.QueryRowContext(ctx, `SELECT session_id FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, name).Scan(&storedSessionID); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			liveID = storedSessionID.String
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, name)
		if err != nil {
			return err
		}
		if count, err = result.RowsAffected(); err != nil || count == 0 {
			return err
		}
		if _, err = tx.ExecContext(ctx, deleteSessionAdmissionSQL, principal.WorkspaceID, principal.SubjectID, name); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, deleteSessionDirSQL, principal.WorkspaceID, principal.SubjectID, name)
		return err
	})
	return count, liveID, err
}

// deleteCatalogContextSession applies the full retention lifecycle to a
// context-backed session exposed through the catalog. Catalog callers may
// select another session owned by the same subject, so the operation is
// scoped by the owner tuple rather than the current principal's session ID.
func (s *SQLite) deleteCatalogContextSession(ctx context.Context, principal contextstate.Principal, sessionID string) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	if err := tombstoneContextSessionTx(ctx, tx, principal, sessionID, contextstate.WorktreeInstance{}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// tombstoneContextSessionTx applies the full retention lifecycle to a live
// context session: tombstone the row, revoke its payloads, write the audit
// and tombstone records, and reclaim its side-table rows. It is the ONE
// implementation both delete paths use - the plain catalog delete and the
// worktree one - so a session cannot be "deleted" in one namespace and stay
// loadable in the other. instance selects the namespace: zero means the
// plain (NULL-instance) row, and a bound row is refused there exactly as
// before; a non-zero instance requires the row to carry that same instance.
// Callers own the transaction and roll back on error.
func tombstoneContextSessionTx(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, sessionID string, instance contextstate.WorktreeInstance) error {
	var revision int
	var instanceID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT session_revision,instance_id FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0`, principal.WorkspaceID, principal.SubjectID, sessionID).Scan(&revision, &instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		// Absent live row. The caller decides: the no-snapshot path treats it
		// as "nothing by that name", while a path that already removed a
		// snapshot treats it as "nothing more to retire" and ignores it.
		return contextstate.ErrSessionNotFound
	}
	if err != nil {
		return err
	}
	if err := requireWorktreeSessionBinding(contextSessionRow{InstanceID: instanceID}, instance); err != nil {
		return err
	}
	auditID, err := newContextID("ctxaudit_")
	if err != nil {
		return err
	}
	created, expires := retentionWindow()
	if _, err = tx.ExecContext(ctx, `UPDATE context_sessions SET tombstoned=1,session_revision=? WHERE workspace_id=? AND subject_id=? AND session_id=? AND tombstoned=0`, revision+1, principal.WorkspaceID, principal.SubjectID, sessionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE context_payloads SET revoked=1,expires_at=? WHERE workspace_id=? AND subject_id=? AND session_id=? AND revoked=0`, expires, principal.WorkspaceID, principal.SubjectID, sessionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO context_audits(audit_id,action,workspace_id,session_id,subject_id,revision,size,retention_class,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, auditID, string(contextstate.AuditDelete), principal.WorkspaceID, sessionID, principal.SubjectID, revision+1, 0, string(contextstate.RetentionCompliance), expires, created); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO context_tombstones(session_id,workspace_id,subject_id,revision,retention_class,expires_at,audit_id,created_at) VALUES(?,?,?,?,?,?,?,?)`, sessionID, principal.WorkspaceID, principal.SubjectID, revision+1, string(contextstate.RetentionCompliance), expires, auditID, created); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, deleteSessionAdmissionSQL, principal.WorkspaceID, principal.SubjectID, sessionID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, deleteSessionDirSQL, principal.WorkspaceID, principal.SubjectID, sessionID)
	return err
}

func (s *SQLite) PruneSessionSnapshots(ctx context.Context, principal contextstate.Principal, names []string) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := validateSessionCatalogName(name); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := rejectManagedCatalogKey(ctx, tx, principal, name); err != nil {
			_ = tx.Rollback()
			return err
		}
		// Reclaim the admission record only for a snapshot this prune actually
		// removed. A name that matched nothing may still be a live
		// context-backed session, and stripping its admitted tool set while it
		// keeps running is the failure mode the delete path already had to fix.
		var pruned int64
		result, err := tx.ExecContext(ctx, `DELETE FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, name)
		if err == nil {
			pruned, err = result.RowsAffected()
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if pruned == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, deleteSessionAdmissionSQL, principal.WorkspaceID, principal.SubjectID, name); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, deleteSessionDirSQL, principal.WorkspaceID, principal.SubjectID, name); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
