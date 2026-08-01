package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// appendSourceEvents is intentionally private: source publication belongs to
// the transactional context store, while imports use a separate all-or-nothing
// adapter.
func (s *SQLite) appendSourceEvents(ctx context.Context, principal contextstate.Principal, events []contextstate.SourceEvent, payloads []contextstate.PayloadRecord) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if !principal.IsBound() {
		return fmt.Errorf("%w: owner capability is not bound", contextstate.ErrPrincipalMismatch)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := authorizeContextSessionTx(ctx, tx, principal, principal.SessionID); err != nil {
		_ = tx.Rollback()
		return err
	}
	var sourceHead uint64
	if err := tx.QueryRowContext(ctx, `SELECT source_sequence FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, principal.SessionID).Scan(&sourceHead); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := contextstate.ValidateSourceEvents(events, principal.SessionID, sourceHead+1); err != nil {
		_ = tx.Rollback()
		return err
	}
	payloadByRef, err := insertContextPayloads(ctx, tx, principal, payloads)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, event := range events {
		if event.PayloadRef != "" {
			if _, ok := payloadByRef[event.PayloadRef]; !ok {
				_ = tx.Rollback()
				return fmt.Errorf("%w: source payload %q was not supplied", contextstate.ErrInvalidDTO, event.PayloadRef)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_source_events(workspace_id,session_id,subject_id,sequence,event_id,kind,role,tool_call_id,payload_ref,payload_namespace,payload_size,provenance,redaction_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, principal.WorkspaceID, principal.SessionID, principal.SubjectID, event.ID.Sequence, sourceEventID(event), event.Kind, event.Role, nullableText(event.ToolCallID), nullableText(event.PayloadRef), nullablePayloadNamespace(event.PayloadRef), event.Size, event.Provenance, event.RedactionStatus); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("append context source event: %w", err)
		}
	}
	if len(events) > 0 {
		last := events[len(events)-1].ID.Sequence
		if _, err := tx.ExecContext(ctx, `UPDATE context_sessions SET source_sequence=? WHERE workspace_id=? AND session_id=?`, last, principal.WorkspaceID, principal.SessionID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) ReadRange(ctx context.Context, principal contextstate.Principal, sourceRange contextstate.SourceRange) ([]contextstate.SourceEvent, error) {
	if err := sourceRange.Validate(); err != nil {
		return nil, err
	}
	if sourceRange.Start.SessionID != principal.SessionID {
		return nil, contextstate.ErrPrincipalMismatch
	}
	if _, err := s.authorizeContextSession(ctx, principal, principal.SessionID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,kind,role,tool_call_id,payload_ref,provenance,redaction_status,payload_size FROM context_source_events WHERE workspace_id=? AND session_id=? AND subject_id=? AND sequence BETWEEN ? AND ? ORDER BY sequence`, principal.WorkspaceID, principal.SessionID, principal.SubjectID, sourceRange.Start.Sequence, sourceRange.End.Sequence)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []contextstate.SourceEvent
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
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *SQLite) ReadPayload(ctx context.Context, principal contextstate.Principal, ref contextstate.ContentRef) (contextstate.SanitizedPayload, error) {
	if err := ref.Validate(); err != nil {
		return contextstate.SanitizedPayload{}, err
	}
	if ref.WorkspaceID != principal.WorkspaceID || ref.SessionID != principal.SessionID || ref.SubjectID != principal.SubjectID {
		return contextstate.SanitizedPayload{}, contextstate.ErrPrincipalMismatch
	}
	if _, err := s.authorizeContextSession(ctx, principal, principal.SessionID); err != nil {
		return contextstate.SanitizedPayload{}, err
	}
	var namespace, workspaceID, sessionID, subjectID, digest, retention, status string
	var size int
	var revoked int
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT namespace,workspace_id,session_id,subject_id,sha256,size,redaction_status,retention_class,revoked,data FROM context_payloads WHERE ref=?`, ref.Ref).Scan(&namespace, &workspaceID, &sessionID, &subjectID, &digest, &size, &status, &retention, &revoked, &data)
	if err == sql.ErrNoRows {
		return contextstate.SanitizedPayload{}, contextstate.ErrPayloadNotFound
	}
	if err != nil {
		return contextstate.SanitizedPayload{}, err
	}
	if namespace != ref.Namespace || workspaceID != principal.WorkspaceID || sessionID != principal.SessionID || subjectID != principal.SubjectID || digest != ref.SHA256 || size != ref.Size {
		return contextstate.SanitizedPayload{}, contextstate.ErrPrincipalMismatch
	}
	if revoked != 0 {
		return contextstate.SanitizedPayload{Ref: ref, Revoked: true, HashOnly: true, Retention: contextstate.RetentionClass(retention)}, contextstate.ErrPayloadRevoked
	}
	result := contextstate.SanitizedPayload{Ref: ref, Retention: contextstate.RetentionClass(retention), HashOnly: data == nil, Dereferenceable: data != nil}
	if data != nil {
		result.Bytes = append([]byte(nil), data...)
	}
	return result, nil
}

type contextSessionRow struct {
	SessionRevision   uint64
	DurableRevision   uint64
	SourceSequence    uint64
	Provider          string
	Model             string
	BindingGeneration uint64
	Tombstoned        bool
}

func (s *SQLite) authorizeContextSession(ctx context.Context, principal contextstate.Principal, sessionID string) (contextSessionRow, error) {
	if err := principal.Validate(); err != nil {
		return contextSessionRow{}, err
	}
	if !principal.IsBound() || sessionID != principal.SessionID {
		return contextSessionRow{}, contextstate.ErrPrincipalMismatch
	}
	var row contextSessionRow
	var subjectID, capability string
	var tombstoned int
	err := s.db.QueryRowContext(ctx, `SELECT subject_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,tombstoned FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, sessionID).Scan(&subjectID, &capability, &row.SessionRevision, &row.DurableRevision, &row.SourceSequence, &row.Provider, &row.Model, &row.BindingGeneration, &tombstoned)
	if err == sql.ErrNoRows {
		return contextSessionRow{}, contextstate.ErrSessionNotFound
	}
	if err != nil {
		return contextSessionRow{}, err
	}
	if subjectID != principal.SubjectID || capability != principal.CapabilityDigest() {
		return contextSessionRow{}, contextstate.ErrPrincipalMismatch
	}
	row.Tombstoned = tombstoned != 0
	if row.Tombstoned {
		return row, contextstate.ErrSessionTombstoned
	}
	return row, nil
}

func authorizeContextSessionTx(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, sessionID string) (contextSessionRow, error) {
	if err := principal.Validate(); err != nil {
		return contextSessionRow{}, err
	}
	if !principal.IsBound() || sessionID != principal.SessionID {
		return contextSessionRow{}, contextstate.ErrPrincipalMismatch
	}
	var row contextSessionRow
	var subjectID, capability string
	var tombstoned int
	err := tx.QueryRowContext(ctx, `SELECT subject_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,tombstoned FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, sessionID).Scan(&subjectID, &capability, &row.SessionRevision, &row.DurableRevision, &row.SourceSequence, &row.Provider, &row.Model, &row.BindingGeneration, &tombstoned)
	if err == sql.ErrNoRows {
		return contextSessionRow{}, contextstate.ErrSessionNotFound
	}
	if err != nil {
		return contextSessionRow{}, err
	}
	if subjectID != principal.SubjectID || capability != principal.CapabilityDigest() {
		return contextSessionRow{}, contextstate.ErrPrincipalMismatch
	}
	row.Tombstoned = tombstoned != 0
	if row.Tombstoned {
		return row, contextstate.ErrSessionTombstoned
	}
	return row, nil
}

func insertContextPayloads(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, payloads []contextstate.PayloadRecord) (map[string]contextstate.ContentRef, error) {
	byRef := make(map[string]contextstate.ContentRef, len(payloads))
	for _, payload := range payloads {
		if err := payload.Validate(); err != nil {
			return nil, err
		}
		if payload.Ref.WorkspaceID != principal.WorkspaceID || payload.Ref.SessionID != principal.SessionID || payload.Ref.SubjectID != principal.SubjectID {
			return nil, contextstate.ErrPrincipalMismatch
		}
		if existing, ok := byRef[payload.Ref.Ref]; ok && existing != payload.Ref {
			return nil, fmt.Errorf("%w: duplicate payload reference", contextstate.ErrInvalidDTO)
		}
		var data any
		if len(payload.Data) > 0 {
			data = payload.Data
		}
		status := "metadata"
		if data != nil {
			status = "sanitized"
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO context_payloads(ref,namespace,workspace_id,session_id,subject_id,sha256,size,redaction_status,retention_class,revoked,data) VALUES(?,?,?,?,?,?,?,?,?,0,?) ON CONFLICT(ref) DO NOTHING`, payload.Ref.Ref, payload.Ref.Namespace, payload.Ref.WorkspaceID, payload.Ref.SessionID, payload.Ref.SubjectID, payload.Ref.SHA256, payload.Ref.Size, status, payload.Retention, data)
		if err != nil {
			return nil, fmt.Errorf("insert context payload: %w", err)
		}
		byRef[payload.Ref.Ref] = payload.Ref
	}
	return byRef, nil
}

func sourceEventID(event contextstate.SourceEvent) string {
	data, _ := contextstate.MarshalCanonical(event)
	digest := sha256.Sum256(data)
	return "ctxe_" + hex.EncodeToString(digest[:])
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullablePayloadNamespace(ref string) any {
	if ref == "" {
		return nil
	}
	return contextstate.Namespace
}
