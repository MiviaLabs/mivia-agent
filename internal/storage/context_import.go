package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/context/state"
)

// ImportSource is the explicit all-or-nothing adapter for legacy JSONL data.
// It is separate from appendSourceEvents so checkpoint publication remains
// private to the context store transaction.
func (s *SQLite) ImportSource(ctx context.Context, principal state.Principal, legacyID, operationKey string, events []state.SourceEvent, payloads []state.PayloadRecord) (state.ImportResult, error) {
	if err := principal.Validate(); err != nil {
		return state.ImportResult{}, err
	}
	if !principal.IsBound() {
		return state.ImportResult{}, state.ErrPrincipalMismatch
	}
	if legacyID == "" || operationKey == "" {
		return state.ImportResult{}, fmt.Errorf("%w: import identity is incomplete", state.ErrInvalidDTO)
	}
	if len(events) == 0 {
		return state.ImportResult{}, fmt.Errorf("%w: import has no source events", state.ErrInvalidDTO)
	}
	fingerprint, err := importFingerprint(events, payloads)
	if err != nil {
		return state.ImportResult{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return state.ImportResult{}, err
	}
	if err := ensureImportedSession(ctx, tx, principal); err != nil {
		_ = tx.Rollback()
		return state.ImportResult{}, err
	}
	row, err := authorizeUnboundContextSessionTx(ctx, tx, principal, principal.SessionID)
	if err != nil {
		_ = tx.Rollback()
		return state.ImportResult{}, err
	}
	if result, found, err := loadImportResult(ctx, tx, principal, operationKey, fingerprint); err != nil {
		_ = tx.Rollback()
		return state.ImportResult{}, err
	} else if found {
		_ = tx.Rollback()
		return result, nil
	}
	if err := state.ValidateSourceEvents(events, principal.SessionID, row.SourceSequence+1); err != nil {
		_ = tx.Rollback()
		return state.ImportResult{}, err
	}
	if _, err := insertContextPayloads(ctx, tx, principal, payloads); err != nil {
		_ = tx.Rollback()
		return state.ImportResult{}, err
	}
	if err := insertImportedEvents(ctx, tx, principal, events); err != nil {
		_ = tx.Rollback()
		return state.ImportResult{}, err
	}
	last := events[len(events)-1].ID.Sequence
	if _, err := tx.ExecContext(ctx, `UPDATE context_sessions SET source_sequence=? WHERE workspace_id=? AND session_id=?`, last, principal.WorkspaceID, principal.SessionID); err != nil {
		_ = tx.Rollback()
		return state.ImportResult{}, err
	}
	rng, _ := state.NewSourceRange(events[0].ID, events[len(events)-1].ID)
	result := state.ImportResult{SessionID: principal.SessionID, SourceRange: rng, Revision: state.Revision{Session: row.SessionRevision, Durable: row.DurableRevision, Source: last}, Imported: len(events), IdempotencyKey: operationKey, Status: "imported", SourceMap: []state.SourceMapping{{LegacyID: legacyID, SessionID: principal.SessionID, SourceStart: events[0].ID, SourceEnd: events[len(events)-1].ID}}, Cutover: state.CutoverState{Mode: "imported", LegacySessionID: legacyID, SessionID: principal.SessionID}, Rollback: state.RollbackToken{SessionID: principal.SessionID, IdempotencyKey: operationKey, Digest: fingerprint}}
	encoded, err := state.MarshalCanonical(result)
	if err != nil {
		_ = tx.Rollback()
		return state.ImportResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_imports(workspace_id,session_id,subject_id,idempotency_key,fingerprint,result) VALUES(?,?,?,?,?,?)`, principal.WorkspaceID, principal.SessionID, principal.SubjectID, operationKey, fingerprint, encoded); err != nil {
		_ = tx.Rollback()
		return state.ImportResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return state.ImportResult{}, err
	}
	return result, nil
}

func ensureImportedSession(ctx context.Context, tx *sql.Tx, principal state.Principal) error {
	var subject, capability string
	err := tx.QueryRowContext(ctx, `SELECT subject_id,capability_digest FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, principal.SessionID).Scan(&subject, &capability)
	if err == nil {
		if subject != principal.SubjectID || capability != principal.CapabilityDigest() {
			return state.ErrPrincipalMismatch
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation) VALUES(?,?,?,?,0,0,0,'legacy','legacy',1)`, principal.WorkspaceID, principal.SubjectID, principal.SessionID, principal.CapabilityDigest())
	return err
}

func insertImportedEvents(ctx context.Context, tx *sql.Tx, principal state.Principal, events []state.SourceEvent) error {
	for _, event := range events {
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_source_events(workspace_id,session_id,subject_id,sequence,event_id,kind,role,tool_call_id,payload_ref,payload_namespace,payload_size,provenance,redaction_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, principal.WorkspaceID, principal.SessionID, principal.SubjectID, event.ID.Sequence, sourceEventID(event), event.Kind, event.Role, nullableText(event.ToolCallID), nullableText(event.PayloadRef), nullablePayloadNamespace(event.PayloadRef), event.Size, event.Provenance, event.RedactionStatus); err != nil {
			return fmt.Errorf("insert imported source event: %w", err)
		}
	}
	return nil
}

func loadImportResult(ctx context.Context, tx *sql.Tx, principal state.Principal, key, fingerprint string) (state.ImportResult, bool, error) {
	var storedFingerprint string
	var data []byte
	err := tx.QueryRowContext(ctx, `SELECT fingerprint,result FROM context_imports WHERE workspace_id=? AND session_id=? AND subject_id=? AND idempotency_key=?`, principal.WorkspaceID, principal.SessionID, principal.SubjectID, key).Scan(&storedFingerprint, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return state.ImportResult{}, false, nil
	}
	if err != nil {
		return state.ImportResult{}, false, err
	}
	if storedFingerprint != fingerprint {
		return state.ImportResult{}, false, state.ErrCheckpointConflict
	}
	var result state.ImportResult
	if err := state.UnmarshalCanonical(data, &result); err != nil {
		return state.ImportResult{}, false, err
	}
	return result, true, nil
}

func importFingerprint(events []state.SourceEvent, payloads []state.PayloadRecord) (string, error) {
	data, err := state.MarshalCanonical(struct {
		Events   []state.SourceEvent   `json:"events"`
		Payloads []state.PayloadRecord `json:"payloads"`
	}{events, payloads})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
