package storage

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

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
		if err := reconcilePayloadBytesTx(ctx, tx, payload, size, existingData, chunkSize); err != nil {
			return nil, err
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

// reconcilePayloadBytesTx settles an incoming record against the stored row.
//
// A ref is minted over the CONTENT's digest, but the bytes are stored only
// when the redaction policy calls them storable. The same ref is therefore
// legitimately written both hash-only and with bytes whenever the effective
// policy differs between two writes. The digest and size checks the caller has
// already made prove this is the same content, so a stored row holding no
// bytes is not a conflict - it is an earlier, less complete record of the same
// thing, and it upgrades.
//
// Refusing it rolled back the WHOLE turn (appendContextCommit -> commitTx),
// losing the user prompt, the assistant reply, and every subagent result in
// it, for one row. That contradicts INV-AG-35, which this package states twice
// elsewhere: a privacy rule may change what is stored, it may never destroy a
// turn the agent already finished. The reverse order was always tolerated by
// the len(payload.Data) > 0 guard; this makes the ref monotone in both
// directions.
func reconcilePayloadBytesTx(ctx context.Context, tx *sql.Tx, payload contextstate.PayloadRecord, size int, existingData []byte, chunkSize int) error {
	if len(payload.Data) == 0 {
		return nil
	}
	existing, err := loadPayloadBytesTx(ctx, tx, payload.Ref.Ref, size, existingData)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return upgradePayloadBytesTx(ctx, tx, payload, chunkSize)
	}
	if !bytes.Equal(payload.Data, existing) {
		return fmt.Errorf("%w: payload reference is held by different bytes", contextstate.ErrCheckpointConflict)
	}
	return nil
}

// upgradePayloadBytesTx fills in the body of a payload row that was first
// recorded hash-only. The caller has already proven, by digest and size, that
// the incoming bytes are the same content the row describes, so this only
// completes a record rather than changing one.
//
// Idempotent: the UPDATE is guarded on data IS NULL, and insertPayloadChunks
// is itself ON CONFLICT DO NOTHING with a byte comparison.
func upgradePayloadBytesTx(ctx context.Context, tx *sql.Tx, payload contextstate.PayloadRecord, chunkSize int) error {
	if len(payload.Data) > chunkSize {
		if err := insertPayloadChunks(ctx, tx, payload.Ref.Ref, payload.Data, chunkSize); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE context_payloads SET redaction_status='sanitized' WHERE ref=? AND data IS NULL`, payload.Ref.Ref); err != nil {
			return fmt.Errorf("upgrade chunked payload: %w", err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE context_payloads SET data=?, redaction_status='sanitized' WHERE ref=? AND data IS NULL`, payload.Data, payload.Ref.Ref); err != nil {
		return fmt.Errorf("upgrade payload: %w", err)
	}
	return nil
}
