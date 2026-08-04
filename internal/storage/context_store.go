package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

const (
	contextFailureAfterSessionCreation = "after-session-creation"
	contextFailurePayloadInsert        = "payload-insert"
	contextFailureSourceAppend         = "source-append"
	contextFailureCheckpointInsert     = "checkpoint-insert"
	contextFailureCompletionMark       = "completion-mark"
	contextFailureActivePointerUpdate  = "active-pointer-update"
	contextFailureRevisionUpdate       = "revision-update"
)

// EnsureSession creates the zero-revision context head and binds it to the
// owner capability. Existing heads are idempotent only for the same owner and
// binding.
func (s *SQLite) EnsureSession(ctx context.Context, request contextstate.EnsureSessionRequest) error {
	if err := validateEnsureRequest(request); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	row, err := readContextHeadTx(ctx, tx, request.Principal)
	if err == nil {
		if row.Tombstoned {
			_ = tx.Rollback()
			return contextstate.ErrSessionTombstoned
		}
		if row.Binding() != request.Binding {
			_ = tx.Rollback()
			return fmt.Errorf("%w: existing session binding differs", contextstate.ErrStaleBinding)
		}
		_ = tx.Rollback()
		return nil
	}
	if !errors.Is(err, contextstate.ErrSessionNotFound) {
		_ = tx.Rollback()
		return err
	}
	var existingSession string
	err = tx.QueryRowContext(ctx, `SELECT session_id FROM context_sessions WHERE session_id=?`, request.Principal.SessionID).Scan(&existingSession)
	if err == nil {
		_ = tx.Rollback()
		return contextstate.ErrPrincipalMismatch
	}
	if !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation) VALUES(?,?,?,?,0,0,0,?,?,?)`, request.Principal.WorkspaceID, request.Principal.SubjectID, request.Principal.SessionID, request.Principal.CapabilityDigest(), request.Binding.Provider, request.Binding.Model, request.Binding.Generation)
	if err != nil {
		_ = tx.Rollback()
		if isConstraint(err) {
			return contextstate.ErrPrincipalMismatch
		}
		return err
	}
	// Record the directory the live session lives in, keyed by session_id in
	// the same side table that holds named-snapshot directories. The TUI
	// restores this directory when the session is reopened.
	if _, err := tx.ExecContext(ctx, upsertSessionDirSQL, request.Principal.WorkspaceID, request.Principal.SubjectID, request.Principal.SessionID, request.Dir, request.Worktree); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := s.contextFailure(contextFailureAfterSessionCreation); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func validateEnsureRequest(request contextstate.EnsureSessionRequest) error {
	if err := request.Principal.Validate(); err != nil {
		return err
	}
	if !request.Principal.IsBound() {
		return fmt.Errorf("%w: owner capability is not bound", contextstate.ErrPrincipalMismatch)
	}
	if !contextstate.ValidSessionDir(request.Dir) || !contextstate.ValidSessionDir(request.Worktree) {
		return fmt.Errorf("%w: invalid session directory metadata", contextstate.ErrInvalidDTO)
	}
	return request.Binding.Validate()
}

func (s *SQLite) Commit(ctx context.Context, request contextstate.CommitRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.OperationID != request.Checkpoint.ID.IdempotencyKey {
		return fmt.Errorf("%w: operation and checkpoint keys differ", contextstate.ErrInvalidDTO)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := s.commitContextTx(ctx, tx, request); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *SQLite) commitContextTx(ctx context.Context, tx *sql.Tx, request contextstate.CommitRequest) error {
	row, err := authorizeContextSessionTx(ctx, tx, request.Principal, request.SessionID)
	if err != nil {
		return err
	}
	known, err := contextOperation(ctx, tx, request.SessionID, request.OperationID)
	if err != nil {
		return err
	}
	if known.Found {
		if known.Kind == "commit" && known.Fingerprint == request.Fingerprint {
			return nil
		}
		return fmt.Errorf("%w: operation key was already used by a %s with different work", contextstate.ErrCheckpointConflict, known.Kind)
	}
	if err := checkContextCAS(row, request.Expected, request.ExpectedBinding); err != nil {
		return err
	}
	return s.appendContextCommit(ctx, tx, request, row)
}

func (s *SQLite) appendContextCommit(ctx context.Context, tx *sql.Tx, request contextstate.CommitRequest, row contextSessionRow) error {
	if err := s.contextFailure(contextFailurePayloadInsert); err != nil {
		return err
	}
	if _, err := insertContextPayloads(ctx, tx, request.Principal, request.Payloads); err != nil {
		return err
	}
	if err := s.contextFailure(contextFailureSourceAppend); err != nil {
		return err
	}
	if err := insertContextSourceEvents(ctx, tx, request.Principal, request.NewSourceEvents); err != nil {
		return err
	}
	return s.publishContextCommit(ctx, tx, request, row)
}

func (s *SQLite) publishContextCommit(ctx context.Context, tx *sql.Tx, request contextstate.CommitRequest, row contextSessionRow) error {
	checkpointID, err := checkpointStorageID(request.Checkpoint.ID)
	if err != nil {
		return err
	}
	if err := s.contextFailure(contextFailureCheckpointInsert); err != nil {
		return err
	}
	if err := insertCheckpoint(ctx, tx, request, checkpointID); err != nil {
		return err
	}
	if err := updateContextSourceHead(ctx, tx, request.Principal, row, request.NewSourceSequence); err != nil {
		return err
	}
	if err := s.contextFailure(contextFailureCompletionMark); err != nil {
		return err
	}
	if err := markCheckpointComplete(ctx, tx, checkpointID); err != nil {
		return err
	}
	if err := s.contextFailure(contextFailureActivePointerUpdate); err != nil {
		return err
	}
	if err := publishContextHead(ctx, tx, request, row, checkpointID); err != nil {
		return err
	}
	if err := s.contextFailure(contextFailureRevisionUpdate); err != nil {
		return err
	}
	return insertContextOperation(ctx, tx, request.SessionID, request.OperationID, request.Fingerprint, "commit")
}

func insertCheckpoint(ctx context.Context, tx *sql.Tx, request contextstate.CommitRequest, storageID string) error {
	digest := sha256.Sum256(request.ActiveContext)
	metadata := request.Checkpoint.SummaryMetadata
	if metadata == nil {
		metadata = []byte{}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO context_checkpoints(checkpoint_id,workspace_id,session_id,subject_id,source_start,source_end,algorithm,schema_version,summary_model,operation_id,idempotency_key,session_revision,durable_revision,binding_generation,turn_id,summary_metadata,active_context,content_fingerprint,complete) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)`, storageID, request.Principal.WorkspaceID, request.SessionID, request.Principal.SubjectID, request.Checkpoint.SourceRange.Start.Sequence, request.Checkpoint.SourceRange.End.Sequence, request.Checkpoint.ID.Algorithm, request.Checkpoint.ID.SchemaVersion, request.Checkpoint.ID.SummaryModel, request.OperationID, request.Checkpoint.ID.IdempotencyKey, request.NewSession, request.NewDurable, request.NewBinding.Generation, request.TurnID, metadata, request.ActiveContext, hex.EncodeToString(digest[:]))
	if err != nil {
		if isConstraint(err) {
			return fmt.Errorf("%w: checkpoint identity is already published", contextstate.ErrCheckpointConflict)
		}
		return fmt.Errorf("insert context checkpoint: %w", err)
	}
	return nil
}

func insertContextSourceEvents(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, events []contextstate.SourceEvent) error {
	for _, event := range events {
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_source_events(workspace_id,session_id,subject_id,sequence,event_id,kind,role,tool_call_id,payload_ref,payload_namespace,payload_size,provenance,redaction_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, principal.WorkspaceID, principal.SessionID, principal.SubjectID, event.ID.Sequence, sourceEventID(event), event.Kind, event.Role, nullableText(event.ToolCallID), nullableText(event.PayloadRef), nullablePayloadNamespace(event.PayloadRef), event.Size, event.Provenance, event.RedactionStatus); err != nil {
			if isConstraint(err) {
				return fmt.Errorf("%w: source sequence %d is already published", contextstate.ErrCheckpointConflict, event.ID.Sequence)
			}
			return fmt.Errorf("append context source event: %w", err)
		}
	}
	return nil
}

func updateContextSourceHead(ctx context.Context, tx *sql.Tx, principal contextstate.Principal, row contextSessionRow, source uint64) error {
	result, err := tx.ExecContext(ctx, `UPDATE context_sessions SET source_sequence=? WHERE workspace_id=? AND session_id=? AND subject_id=? AND capability_digest=? AND session_revision=? AND durable_revision=? AND source_sequence=? AND provider=? AND model=? AND binding_generation=? AND tombstoned=0`, source, principal.WorkspaceID, principal.SessionID, principal.SubjectID, principal.CapabilityDigest(), row.SessionRevision, row.DurableRevision, row.SourceSequence, row.Provider, row.Model, row.BindingGeneration)
	if err != nil {
		return err
	}
	return requireContextRows(result, contextstate.ErrStaleRevision)
}

func markCheckpointComplete(ctx context.Context, tx *sql.Tx, storageID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE context_checkpoints SET complete=1 WHERE checkpoint_id=? AND complete=0`, storageID)
	if err != nil {
		return err
	}
	return requireContextRows(result, fmt.Errorf("%w: checkpoint was already completed", contextstate.ErrCheckpointConflict))
}

func publishContextHead(ctx context.Context, tx *sql.Tx, request contextstate.CommitRequest, row contextSessionRow, checkpointID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE context_sessions SET session_revision=?,durable_revision=?,provider=?,model=?,binding_generation=?,active_checkpoint_id=? WHERE workspace_id=? AND session_id=? AND subject_id=? AND capability_digest=? AND session_revision=? AND durable_revision=? AND source_sequence=? AND provider=? AND model=? AND binding_generation=? AND tombstoned=0`, request.NewSession, request.NewDurable, request.NewBinding.Provider, request.NewBinding.Model, request.NewBinding.Generation, checkpointID, request.Principal.WorkspaceID, request.SessionID, request.Principal.SubjectID, request.Principal.CapabilityDigest(), row.SessionRevision, row.DurableRevision, request.NewSourceSequence, row.Provider, row.Model, row.BindingGeneration)
	if err != nil {
		return err
	}
	return requireContextRows(result, contextstate.ErrStaleRevision)
}

func (s *SQLite) Advance(ctx context.Context, request contextstate.AdvanceRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	fingerprint, err := fingerprintAdvance(request)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	row, err := authorizeContextSessionTx(ctx, tx, request.Principal, request.SessionID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	known, err := contextOperation(ctx, tx, request.SessionID, request.OperationID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if known.Found {
		_ = tx.Rollback()
		if known.Kind == "advance" && known.Fingerprint == fingerprint {
			return nil
		}
		return contextstate.ErrCheckpointConflict
	}
	if err := checkContextCAS(row, request.Expected, request.ExpectedBinding); err != nil {
		_ = tx.Rollback()
		return err
	}
	activeID, err := advanceActiveCheckpoint(ctx, tx, request, row.SourceSequence)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := s.contextFailure(contextFailureActivePointerUpdate); err != nil {
		_ = tx.Rollback()
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE context_sessions SET session_revision=?,durable_revision=?,provider=?,model=?,binding_generation=?,active_checkpoint_id=? WHERE workspace_id=? AND session_id=? AND subject_id=? AND capability_digest=? AND session_revision=? AND durable_revision=? AND source_sequence=? AND provider=? AND model=? AND binding_generation=? AND tombstoned=0`, request.NewSession, request.NewDurable, request.NewBinding.Provider, request.NewBinding.Model, request.NewBinding.Generation, activeID, request.Principal.WorkspaceID, request.SessionID, request.Principal.SubjectID, request.Principal.CapabilityDigest(), row.SessionRevision, row.DurableRevision, row.SourceSequence, row.Provider, row.Model, row.BindingGeneration)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := requireContextRows(result, contextstate.ErrStaleRevision); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := s.contextFailure(contextFailureRevisionUpdate); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := insertContextOperation(ctx, tx, request.SessionID, request.OperationID, fingerprint, "advance"); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func advanceActiveCheckpoint(ctx context.Context, tx *sql.Tx, request contextstate.AdvanceRequest, source uint64) (any, error) {
	if request.ClearActive {
		return nil, nil
	}
	if request.ActiveCheckpointID == "" {
		var current sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT active_checkpoint_id FROM context_sessions WHERE workspace_id=? AND session_id=?`, request.Principal.WorkspaceID, request.SessionID).Scan(&current); err != nil {
			return nil, err
		}
		if current.Valid {
			return current.String, nil
		}
		return nil, nil
	}
	var checkpointSource uint64
	err := tx.QueryRowContext(ctx, `SELECT source_end FROM context_checkpoints WHERE checkpoint_id=? AND workspace_id=? AND session_id=? AND subject_id=? AND complete=1`, request.ActiveCheckpointID, request.Principal.WorkspaceID, request.SessionID, request.Principal.SubjectID).Scan(&checkpointSource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, contextstate.ErrCheckpointConflict
	}
	if err != nil {
		return nil, err
	}
	if checkpointSource > source {
		return nil, contextstate.ErrCheckpointConflict
	}
	return request.ActiveCheckpointID, nil
}

func (s *SQLite) Load(ctx context.Context, principal contextstate.Principal, sessionID string) (contextstate.Snapshot, error) {
	if err := principal.Validate(); err != nil {
		return contextstate.Snapshot{}, err
	}
	if !principal.IsBound() || sessionID != principal.SessionID {
		return contextstate.Snapshot{}, contextstate.ErrPrincipalMismatch
	}
	row, err := readContextHead(ctx, s.db, principal)
	if err != nil {
		return contextstate.Snapshot{}, err
	}
	if row.Tombstoned {
		return contextstate.Snapshot{}, contextstate.ErrSessionTombstoned
	}
	source, err := readContextSourceEvents(ctx, s.db, principal)
	if err != nil {
		return contextstate.Snapshot{}, err
	}
	active, found, err := recoverActive(ctx, s.db, principal, row)
	if err != nil {
		return contextstate.Snapshot{}, err
	}
	snapshot := contextstate.Snapshot{Revision: row.Revision(), Binding: row.Binding(), Source: source, Tombstoned: false}
	if found {
		snapshot.Active = active
	}
	return snapshot, nil
}

type contextOperationRecord struct {
	Fingerprint string
	Kind        string
	Found       bool
}

func contextOperation(ctx context.Context, tx *sql.Tx, sessionID, operationID string) (contextOperationRecord, error) {
	var record contextOperationRecord
	err := tx.QueryRowContext(ctx, `SELECT fingerprint,kind FROM context_operations WHERE session_id=? AND operation_id=?`, sessionID, operationID).Scan(&record.Fingerprint, &record.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	record.Found = true
	return record, nil
}

func insertContextOperation(ctx context.Context, tx *sql.Tx, sessionID, operationID, fingerprint, kind string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO context_operations(session_id,operation_id,fingerprint,kind) VALUES(?,?,?,?)`, sessionID, operationID, fingerprint, kind)
	if err != nil && isConstraint(err) {
		return fmt.Errorf("%w: %s operation key is already recorded", contextstate.ErrCheckpointConflict, kind)
	}
	return err
}

func fingerprintAdvance(request contextstate.AdvanceRequest) (string, error) {
	data, err := contextstate.MarshalCanonical(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func checkContextCAS(row contextSessionRow, expected contextstate.Revision, binding contextstate.BindingRevision) error {
	if row.Revision() != expected {
		return fmt.Errorf("%w: context head changed", contextstate.ErrStaleRevision)
	}
	if row.Binding() != binding {
		return fmt.Errorf("%w: context binding changed", contextstate.ErrStaleBinding)
	}
	return nil
}

func requireContextRows(result sql.Result, conflict error) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return conflict
	}
	return nil
}

func (s *SQLite) contextFailure(step string) error {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	if s.contextFailureStep != step {
		return nil
	}
	s.contextFailureStep = ""
	return fmt.Errorf("injected context failure at %s", step)
}

func (s *SQLite) injectContextFailure(step string) {
	s.failureMu.Lock()
	s.contextFailureStep = step
	s.failureMu.Unlock()
}
