package storage

import (
	"bytes"
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
	// Prefer inline single-BLOB when present (small payloads + pre-chunk rows).
	// Otherwise reassemble ordered chunks under this content ref.
	if data == nil {
		reassembled, rerr := readPayloadChunks(ctx, s.db, ref.Ref, size)
		if rerr != nil {
			return contextstate.SanitizedPayload{}, rerr
		}
		data = reassembled
	}
	if data != nil {
		payloadDigest := sha256.Sum256(data)
		if len(data) != size || hex.EncodeToString(payloadDigest[:]) != ref.SHA256 {
			return contextstate.SanitizedPayload{}, fmt.Errorf("%w: stored payload digest mismatch", contextstate.ErrInvalidDTO)
		}
	}
	result := contextstate.SanitizedPayload{Ref: ref, Retention: contextstate.RetentionClass(retention), HashOnly: data == nil, Dereferenceable: data != nil}
	if data != nil {
		result.Bytes = append([]byte(nil), data...)
	}
	return result, nil
}

// readPayloadChunks reassembles an ordered chunk sequence. Mismatch against
// expected size fails closed (caller still verifies full-payload SHA-256).
// q may be *sql.DB or *sql.Tx (same QueryContext surface as contextQueryer).
func readPayloadChunks(ctx context.Context, q contextQueryer, ref string, expectedSize int) ([]byte, error) {
	rows, err := q.QueryContext(ctx, `SELECT chunk_index, chunk_count, data FROM context_payload_chunks WHERE ref=? ORDER BY chunk_index`, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var (
		parts       [][]byte
		seen        int
		total       int
		expectCount int
	)
	for rows.Next() {
		var idx, chunkCount int
		var chunk []byte
		if err := rows.Scan(&idx, &chunkCount, &chunk); err != nil {
			return nil, err
		}
		if seen == 0 {
			expectCount = chunkCount
			if expectCount <= 0 {
				return nil, fmt.Errorf("%w: invalid chunk_count", contextstate.ErrInvalidDTO)
			}
			parts = make([][]byte, expectCount)
		} else if chunkCount != expectCount {
			return nil, fmt.Errorf("%w: inconsistent chunk_count", contextstate.ErrInvalidDTO)
		}
		if idx < 0 || idx >= expectCount {
			return nil, fmt.Errorf("%w: chunk_index out of range", contextstate.ErrInvalidDTO)
		}
		if parts[idx] != nil {
			return nil, fmt.Errorf("%w: duplicate chunk_index", contextstate.ErrInvalidDTO)
		}
		parts[idx] = chunk
		total += len(chunk)
		seen++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if seen == 0 {
		// No inline data and no chunks: hash-only / empty body.
		return nil, nil
	}
	if seen != expectCount {
		return nil, fmt.Errorf("%w: incomplete chunk sequence (%d of %d)", contextstate.ErrInvalidDTO, seen, expectCount)
	}
	if total != expectedSize {
		return nil, fmt.Errorf("%w: reassembled size mismatch", contextstate.ErrInvalidDTO)
	}
	out := make([]byte, 0, total)
	for i, p := range parts {
		if p == nil {
			return nil, fmt.Errorf("%w: missing chunk %d", contextstate.ErrInvalidDTO, i)
		}
		out = append(out, p...)
	}
	return out, nil
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
	chunkSize := contextstate.PayloadChunkSize()
	for _, payload := range payloads {
		if err := payload.Validate(); err != nil {
			return nil, err
		}
		if payload.Ref.WorkspaceID != principal.WorkspaceID || payload.Ref.SessionID != principal.SessionID || payload.Ref.SubjectID != principal.SubjectID {
			return nil, contextstate.ErrPrincipalMismatch
		}
		if payload.Revoked {
			return nil, contextstate.ErrPayloadRevoked
		}
		if existing, ok := byRef[payload.Ref.Ref]; ok && existing != payload.Ref {
			return nil, fmt.Errorf("%w: duplicate payload reference", contextstate.ErrInvalidDTO)
		}
		// Split large bodies into ordered chunks under one content ref.
		// Small payloads stay as a single BLOB in context_payloads.data.
		useChunks := len(payload.Data) > chunkSize
		var inline any
		status := "metadata"
		if len(payload.Data) > 0 {
			status = "sanitized"
			if !useChunks {
				inline = payload.Data
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO context_payloads(ref,namespace,workspace_id,session_id,subject_id,sha256,size,redaction_status,retention_class,revoked,data) VALUES(?,?,?,?,?,?,?,?,?,0,?) ON CONFLICT(ref) DO NOTHING`, payload.Ref.Ref, payload.Ref.Namespace, payload.Ref.WorkspaceID, payload.Ref.SessionID, payload.Ref.SubjectID, payload.Ref.SHA256, payload.Ref.Size, status, payload.Retention, inline)
		if err != nil {
			return nil, fmt.Errorf("insert context payload: %w", err)
		}
		if useChunks {
			if err := insertPayloadChunks(ctx, tx, payload.Ref.Ref, payload.Data, chunkSize); err != nil {
				return nil, err
			}
		}
		var namespace, workspaceID, sessionID, subjectID, digest, retention string
		var size, revoked int
		var existingData []byte
		if err := tx.QueryRowContext(ctx, `SELECT namespace,workspace_id,session_id,subject_id,sha256,size,retention_class,revoked,data FROM context_payloads WHERE ref=?`, payload.Ref.Ref).Scan(&namespace, &workspaceID, &sessionID, &subjectID, &digest, &size, &retention, &revoked, &existingData); err != nil {
			return nil, err
		}
		if namespace != payload.Ref.Namespace || workspaceID != payload.Ref.WorkspaceID || sessionID != payload.Ref.SessionID || subjectID != payload.Ref.SubjectID || digest != payload.Ref.SHA256 || size != payload.Ref.Size || retention != string(payload.Retention) {
			return nil, fmt.Errorf("%w: payload reference is held by a different owner or content", contextstate.ErrCheckpointConflict)
		}
		if revoked != 0 {
			return nil, contextstate.ErrPayloadRevoked
		}
		if len(payload.Data) > 0 {
			existing, err := loadPayloadBytesTx(ctx, tx, payload.Ref.Ref, size, existingData)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(payload.Data, existing) {
				return nil, fmt.Errorf("%w: payload reference is held by different bytes", contextstate.ErrCheckpointConflict)
			}
		}
		byRef[payload.Ref.Ref] = payload.Ref
	}
	return byRef, nil
}

// insertPayloadChunks writes an ordered chunk sequence. Idempotent on
// (ref, chunk_index): ON CONFLICT DO NOTHING, then verify bytes match.
//
// When the existing layout differs (e.g. PayloadChunkSize changed) but the
// reassembled full body equals data, accept and leave the old layout in place.
// Only report ErrCheckpointConflict when the full body differs.
func insertPayloadChunks(ctx context.Context, tx *sql.Tx, ref string, data []byte, chunkSize int) error {
	if chunkSize <= 0 {
		chunkSize = contextstate.DefaultPayloadChunkBytes
	}
	if len(data) == 0 {
		return nil
	}
	count := (len(data) + chunkSize - 1) / chunkSize
	for i := 0; i < count; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[start:end]
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_payload_chunks(ref,chunk_index,chunk_count,data) VALUES(?,?,?,?) ON CONFLICT(ref, chunk_index) DO NOTHING`, ref, i, count, chunk); err != nil {
			return fmt.Errorf("insert payload chunk %d: %w", i, err)
		}
		var stored []byte
		var storedCount int
		if err := tx.QueryRowContext(ctx, `SELECT chunk_count, data FROM context_payload_chunks WHERE ref=? AND chunk_index=?`, ref, i).Scan(&storedCount, &stored); err != nil {
			return err
		}
		if storedCount != count || !bytes.Equal(stored, chunk) {
			// Layout or per-chunk mismatch: accept only if full body is identical.
			existing, err := loadPayloadBytesTx(ctx, tx, ref, len(data), nil)
			if err != nil {
				return err
			}
			if bytes.Equal(data, existing) {
				return nil
			}
			return fmt.Errorf("%w: payload chunk %d conflict", contextstate.ErrCheckpointConflict, i)
		}
	}
	return nil
}

func loadPayloadBytesTx(ctx context.Context, tx *sql.Tx, ref string, size int, inline []byte) ([]byte, error) {
	if inline != nil {
		return inline, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT chunk_index, chunk_count, data FROM context_payload_chunks WHERE ref=? ORDER BY chunk_index`, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var parts [][]byte
	var expectCount, seen, total int
	for rows.Next() {
		var idx, chunkCount int
		var chunk []byte
		if err := rows.Scan(&idx, &chunkCount, &chunk); err != nil {
			return nil, err
		}
		if seen == 0 {
			expectCount = chunkCount
			parts = make([][]byte, expectCount)
		}
		if idx < 0 || idx >= expectCount || parts[idx] != nil {
			return nil, fmt.Errorf("%w: bad chunk sequence", contextstate.ErrInvalidDTO)
		}
		parts[idx] = chunk
		total += len(chunk)
		seen++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if seen == 0 {
		return nil, nil
	}
	if seen != expectCount || total != size {
		return nil, fmt.Errorf("%w: incomplete stored payload", contextstate.ErrInvalidDTO)
	}
	out := make([]byte, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out, nil
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
