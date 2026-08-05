package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

type contextHead struct {
	contextSessionRow
	ActiveCheckpointID sql.NullString
}

func (r contextSessionRow) Revision() contextstate.Revision {
	return contextstate.Revision{Session: r.SessionRevision, Durable: r.DurableRevision, Source: r.SourceSequence}
}

func (r contextSessionRow) Binding() contextstate.BindingRevision {
	return contextstate.BindingRevision{Provider: r.Provider, Model: r.Model, Generation: r.BindingGeneration}
}

func readContextHead(ctx context.Context, q contextRowQueryer, principal contextstate.Principal) (contextHead, error) {
	return scanContextHead(q.QueryRowContext(ctx, `SELECT subject_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,active_checkpoint_id,tombstoned,instance_id FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, principal.SessionID), principal)
}

func readContextHeadTx(ctx context.Context, tx *sql.Tx, principal contextstate.Principal) (contextHead, error) {
	return scanContextHead(tx.QueryRowContext(ctx, `SELECT subject_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,active_checkpoint_id,tombstoned,instance_id FROM context_sessions WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, principal.SessionID), principal)
}

type contextRowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanContextHead(row *sql.Row, principal contextstate.Principal) (contextHead, error) {
	var head contextHead
	var subjectID, capability string
	var tombstoned int
	err := row.Scan(&subjectID, &capability, &head.SessionRevision, &head.DurableRevision, &head.SourceSequence, &head.Provider, &head.Model, &head.BindingGeneration, &head.ActiveCheckpointID, &tombstoned, &head.InstanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return contextHead{}, contextstate.ErrSessionNotFound
	}
	if err != nil {
		return contextHead{}, err
	}
	if subjectID != principal.SubjectID || capability != principal.CapabilityDigest() {
		return contextHead{}, contextstate.ErrPrincipalMismatch
	}
	head.Tombstoned = tombstoned != 0
	return head, nil
}

func recoverActive(ctx context.Context, q contextRowQueryer, principal contextstate.Principal, head contextHead) (contextstate.CheckpointRecord, bool, error) {
	if head.ActiveCheckpointID.Valid {
		checkpoint, err := readCheckpoint(ctx, q, principal, head.Provider, head.Model, `checkpoint_id=?`, head.ActiveCheckpointID.String)
		if err == nil && checkpoint.Complete && checkpoint.Revision.Session <= head.SessionRevision && checkpoint.Revision.Durable <= head.DurableRevision && checkpoint.SourceRange.End.Sequence <= head.SourceSequence {
			return checkpoint, true, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return contextstate.CheckpointRecord{}, false, err
		}
	}
	checkpoint, err := readCheckpoint(ctx, q, principal, head.Provider, head.Model, `complete=1 AND source_end<=? AND session_revision=? AND durable_revision=? ORDER BY source_end DESC LIMIT 1`, head.SourceSequence, head.SessionRevision, head.DurableRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return contextstate.CheckpointRecord{}, false, nil
	}
	if err != nil {
		return contextstate.CheckpointRecord{}, false, err
	}
	return checkpoint, true, nil
}

func readCheckpoint(ctx context.Context, q contextRowQueryer, principal contextstate.Principal, provider, model, predicate string, args ...any) (contextstate.CheckpointRecord, error) {
	query := `SELECT source_start,source_end,algorithm,schema_version,summary_model,idempotency_key,session_revision,durable_revision,binding_generation,turn_id,summary_metadata,active_context,content_fingerprint,complete FROM context_checkpoints WHERE workspace_id=? AND session_id=? AND subject_id=? AND ` + predicate
	queryArgs := append([]any{principal.WorkspaceID, principal.SessionID, principal.SubjectID}, args...)
	var start, end, sessionRevision, durableRevision, generation, turnID uint64
	var algorithm, summaryModel, key, fingerprint string
	var schemaVersion uint32
	var metadata, active []byte
	var complete int
	err := q.QueryRowContext(ctx, query, queryArgs...).Scan(&start, &end, &algorithm, &schemaVersion, &summaryModel, &key, &sessionRevision, &durableRevision, &generation, &turnID, &metadata, &active, &fingerprint, &complete)
	if err != nil {
		return contextstate.CheckpointRecord{}, err
	}
	rng := contextstate.SourceRange{Start: contextstate.SourceID{SessionID: principal.SessionID, Sequence: start}, End: contextstate.SourceID{SessionID: principal.SessionID, Sequence: end}}
	id := contextstate.CheckpointID{SessionID: principal.SessionID, SourceRange: rng, Algorithm: algorithm, SchemaVersion: schemaVersion, SummaryModel: summaryModel, IdempotencyKey: key}
	checkpoint := contextstate.CheckpointRecord{ID: id, Revision: contextstate.Revision{Session: sessionRevision, Durable: durableRevision, Source: end}, Binding: contextstate.BindingRevision{Provider: provider, Model: model, Generation: generation}, SourceRange: rng, ActiveContext: append([]byte(nil), active...), SummaryMetadata: append([]byte(nil), metadata...), TurnID: turnID, Complete: complete != 0}
	if complete != 0 {
		if err := validateStoredCheckpoint(checkpoint, fingerprint); err != nil {
			return contextstate.CheckpointRecord{}, err
		}
	}
	return checkpoint, nil
}

func validateStoredCheckpoint(checkpoint contextstate.CheckpointRecord, fingerprint string) error {
	if len(fingerprint) != 64 {
		return fmt.Errorf("%w: checkpoint fingerprint is invalid", contextstate.ErrInvalidDTO)
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return fmt.Errorf("%w: checkpoint fingerprint is invalid", contextstate.ErrInvalidDTO)
	}
	digest := sha256.Sum256(checkpoint.ActiveContext)
	if hex.EncodeToString(digest[:]) != strings.ToLower(fingerprint) {
		return fmt.Errorf("%w: checkpoint fingerprint does not match active context", contextstate.ErrInvalidDTO)
	}
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	return nil
}

func checkpointStorageID(id contextstate.CheckpointID) (string, error) {
	data, err := contextstate.MarshalCanonical(id)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "ctxc_" + hex.EncodeToString(digest[:]), nil
}
