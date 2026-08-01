package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func (s *SQLite) DeleteSession(ctx context.Context, principal contextstate.Principal, sessionID string) (contextstate.DeleteResult, error) {
	if sessionID != principal.SessionID {
		return contextstate.DeleteResult{}, contextstate.ErrPrincipalMismatch
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contextstate.DeleteResult{}, err
	}
	row, authErr := authorizeContextSessionTx(ctx, tx, principal, sessionID)
	if errors.Is(authErr, contextstate.ErrSessionTombstoned) {
		_ = tx.Rollback()
		return s.existingDeleteResult(ctx, principal)
	}
	if authErr != nil {
		_ = tx.Rollback()
		return contextstate.DeleteResult{}, authErr
	}
	auditID, err := newContextID("ctxaudit_")
	if err != nil {
		_ = tx.Rollback()
		return contextstate.DeleteResult{}, err
	}
	created, expires := retentionWindow()
	if _, err := tx.ExecContext(ctx, `UPDATE context_sessions SET tombstoned=1,session_revision=? WHERE workspace_id=? AND session_id=? AND tombstoned=0`, row.SessionRevision+1, principal.WorkspaceID, sessionID); err != nil {
		_ = tx.Rollback()
		return contextstate.DeleteResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE context_payloads SET revoked=1,expires_at=? WHERE workspace_id=? AND session_id=? AND subject_id=? AND revoked=0`, expires, principal.WorkspaceID, sessionID, principal.SubjectID)
	if err != nil {
		_ = tx.Rollback()
		return contextstate.DeleteResult{}, err
	}
	revoked, _ := result.RowsAffected()
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_audits(audit_id,action,workspace_id,session_id,subject_id,revision,size,retention_class,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, auditID, string(contextstate.AuditDelete), principal.WorkspaceID, sessionID, principal.SubjectID, row.SessionRevision+1, 0, string(contextstate.RetentionCompliance), expires, created); err != nil {
		_ = tx.Rollback()
		return contextstate.DeleteResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_tombstones(session_id,workspace_id,subject_id,revision,retention_class,expires_at,audit_id,created_at) VALUES(?,?,?,?,?,?,?,?)`, sessionID, principal.WorkspaceID, principal.SubjectID, row.SessionRevision+1, string(contextstate.RetentionCompliance), expires, auditID, created); err != nil {
		_ = tx.Rollback()
		return contextstate.DeleteResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return contextstate.DeleteResult{}, err
	}
	return contextstate.DeleteResult{SessionID: sessionID, TombstoneRevision: contextstate.Revision{Session: row.SessionRevision + 1, Durable: row.DurableRevision, Source: row.SourceSequence}, RevokedRefs: int(revoked), AuditID: auditID}, nil
}

func (s *SQLite) ExportSession(ctx context.Context, principal contextstate.Principal, sessionID string) (contextstate.ExportResult, error) {
	if sessionID != principal.SessionID {
		return contextstate.ExportResult{}, contextstate.ErrPrincipalMismatch
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contextstate.ExportResult{}, err
	}
	row, err := authorizeContextSessionTx(ctx, tx, principal, sessionID)
	if err != nil {
		_ = tx.Rollback()
		return contextstate.ExportResult{}, err
	}
	source, err := readContextSourceEvents(ctx, tx, principal)
	if err != nil {
		_ = tx.Rollback()
		return contextstate.ExportResult{}, err
	}
	payloads, err := readContextExportPayloads(ctx, tx, principal)
	if err != nil {
		_ = tx.Rollback()
		return contextstate.ExportResult{}, err
	}
	envelope := contextExportEnvelope{Version: 1, SessionID: sessionID, Revision: contextstate.Revision{Session: row.SessionRevision, Durable: row.DurableRevision, Source: row.SourceSequence}, Source: source, Payloads: payloads}
	records, err := contextstate.MarshalCanonical(envelope)
	if err != nil {
		_ = tx.Rollback()
		return contextstate.ExportResult{}, err
	}
	if len(records) > contextstate.MaxExportBytes {
		_ = tx.Rollback()
		return contextstate.ExportResult{}, contextstate.ErrExportTooLarge
	}
	auditID, err := newContextID("ctxaudit_")
	if err != nil {
		_ = tx.Rollback()
		return contextstate.ExportResult{}, err
	}
	created, expires := retentionWindow()
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_audits(audit_id,action,workspace_id,session_id,subject_id,revision,size,retention_class,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, auditID, string(contextstate.AuditExport), principal.WorkspaceID, sessionID, principal.SubjectID, row.DurableRevision, len(records), string(contextstate.RetentionCompliance), expires, created); err != nil {
		_ = tx.Rollback()
		return contextstate.ExportResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return contextstate.ExportResult{}, err
	}
	return contextstate.ExportResult{SessionID: sessionID, Revision: envelope.Revision, Records: records, Count: len(source) + len(payloads), AuditID: auditID}, nil
}

type contextExportEnvelope struct {
	Version   int                        `json:"version"`
	SessionID string                     `json:"session_id"`
	Revision  contextstate.Revision      `json:"revision"`
	Source    []contextstate.SourceEvent `json:"source"`
	Payloads  []contextExportPayload     `json:"payloads"`
}

type contextExportPayload struct {
	Ref       contextstate.ContentRef     `json:"ref"`
	Bytes     []byte                      `json:"bytes,omitempty"`
	HashOnly  bool                        `json:"hash_only"`
	Retention contextstate.RetentionClass `json:"retention"`
}

type contextQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readContextSourceEvents(ctx context.Context, q contextQueryer, principal contextstate.Principal) ([]contextstate.SourceEvent, error) {
	rows, err := q.QueryContext(ctx, `SELECT sequence,kind,role,tool_call_id,payload_ref,provenance,redaction_status,payload_size FROM context_source_events WHERE workspace_id=? AND session_id=? AND subject_id=? ORDER BY sequence`, principal.WorkspaceID, principal.SessionID, principal.SubjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contextstate.SourceEvent
	for rows.Next() {
		var sequence uint64
		var kind, role, provenance, status string
		var toolCallID, payloadRef sql.NullString
		var size int
		if err := rows.Scan(&sequence, &kind, &role, &toolCallID, &payloadRef, &provenance, &status, &size); err != nil {
			return nil, err
		}
		event := contextstate.SourceEvent{ID: contextstate.SourceID{SessionID: principal.SessionID, Sequence: sequence}, Kind: kind, Role: role, Provenance: provenance, RedactionStatus: status, Size: size}
		if toolCallID.Valid {
			event.ToolCallID = toolCallID.String
		}
		if payloadRef.Valid {
			event.PayloadRef = payloadRef.String
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func readContextExportPayloads(ctx context.Context, q contextQueryer, principal contextstate.Principal) ([]contextExportPayload, error) {
	rows, err := q.QueryContext(ctx, `SELECT ref,namespace,sha256,size,retention_class,data FROM context_payloads WHERE workspace_id=? AND session_id=? AND subject_id=? AND revoked=0 ORDER BY ref`, principal.WorkspaceID, principal.SessionID, principal.SubjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contextExportPayload
	for rows.Next() {
		var ref, namespace, digest, retention string
		var size int
		var data []byte
		if err := rows.Scan(&ref, &namespace, &digest, &size, &retention, &data); err != nil {
			return nil, err
		}
		contentRef := contextstate.ContentRef{Ref: ref, Namespace: namespace, SHA256: digest, WorkspaceID: principal.WorkspaceID, SessionID: principal.SessionID, SubjectID: principal.SubjectID, Size: size}
		if err := contentRef.Validate(); err != nil {
			return nil, err
		}
		out = append(out, contextExportPayload{Ref: contentRef, Bytes: append([]byte(nil), data...), HashOnly: data == nil, Retention: contextstate.RetentionClass(retention)})
	}
	return out, rows.Err()
}

func (s *SQLite) existingDeleteResult(ctx context.Context, principal contextstate.Principal) (contextstate.DeleteResult, error) {
	var result contextstate.DeleteResult
	var sessionRevision, durableRevision, sourceSequence uint64
	var auditID string
	var revoked int
	err := s.db.QueryRowContext(ctx, `SELECT s.session_revision,s.durable_revision,s.source_sequence,t.audit_id,(SELECT COUNT(*) FROM context_payloads p WHERE p.workspace_id=s.workspace_id AND p.session_id=s.session_id AND p.subject_id=s.subject_id AND p.revoked=1) FROM context_sessions s JOIN context_tombstones t ON t.session_id=s.session_id AND t.workspace_id=s.workspace_id AND t.subject_id=s.subject_id WHERE s.workspace_id=? AND s.session_id=? AND s.subject_id=? AND s.capability_digest=?`, principal.WorkspaceID, principal.SessionID, principal.SubjectID, principal.CapabilityDigest()).Scan(&sessionRevision, &durableRevision, &sourceSequence, &auditID, &revoked)
	if err == sql.ErrNoRows {
		return contextstate.DeleteResult{}, contextstate.ErrSessionNotFound
	}
	if err != nil {
		return contextstate.DeleteResult{}, err
	}
	result.SessionID = principal.SessionID
	result.TombstoneRevision = contextstate.Revision{Session: sessionRevision, Durable: durableRevision, Source: sourceSequence}
	result.RevokedRefs = revoked
	result.AuditID = auditID
	return result, nil
}

func retentionWindow() (string, string) {
	now := time.Now().UTC()
	return now.Format(time.RFC3339Nano), now.Add(365 * 24 * time.Hour).Format(time.RFC3339Nano)
}

func newContextID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate context identifier: %w", err)
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

// PruneContextPayloads removes only revoked payload rows whose retention
// expiry has elapsed. Tombstones and audit records are compliance records and
// are intentionally outside this maintenance operation.
func (s *SQLite) PruneContextPayloads(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 || limit > 10_000 {
		return 0, fmt.Errorf("%w: invalid payload GC limit", contextstate.ErrInvalidDTO)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM context_payloads WHERE ref IN (SELECT ref FROM context_payloads WHERE revoked=1 AND expires_at <= ? ORDER BY ref LIMIT ?)`, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	return int(count), nil
}
