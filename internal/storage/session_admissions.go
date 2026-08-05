package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

var _ contextstate.SessionAdmissionCatalog = (*SQLite)(nil)
var _ contextstate.WorktreeAdmissionCatalog = (*SQLite)(nil)

// SaveSessionAdmission persists a named session's admitted tool set. An empty
// name set deletes the row: resuming a session that admitted nothing must not
// resurrect an older set.
func (s *SQLite) SaveSessionAdmission(ctx context.Context, principal contextstate.Principal, name string, record contextstate.SessionAdmission) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := validateSessionCatalogName(name); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// A []string always marshals; there is no error branch to test here, so
	// there is none to write.
	encoded, _ := json.Marshal(record.Names)
	if contextstate.Exceeds(len(encoded), contextstate.CurrentLimits().SessionStateBytes) {
		return fmt.Errorf("%w: admitted tool set is too large", contextstate.ErrInvalidDTO)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := rejectManagedCatalogKey(ctx, tx, principal, name); err != nil {
			return err
		}
		if len(record.Names) == 0 {
			_, err := tx.ExecContext(ctx, deleteSessionAdmissionSQL, principal.WorkspaceID, principal.SubjectID, name)
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `INSERT INTO chat_session_admissions(workspace_id,subject_id,name,agent,digest,names,updated_at,instance_id) VALUES(?,?,?,?,?,?,?,NULL) ON CONFLICT(workspace_id,subject_id,name) DO UPDATE SET agent=excluded.agent,digest=excluded.digest,names=excluded.names,updated_at=excluded.updated_at WHERE chat_session_admissions.instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, name, record.Agent, record.Digest, string(encoded), now)
		if err != nil {
			return err
		}
		return requireCatalogMutation(result)
	})
}

// LoadSessionAdmission returns the stored admission record. A session with no
// row yields the zero value and a nil error: no admissions is a normal state.
func (s *SQLite) LoadSessionAdmission(ctx context.Context, principal contextstate.Principal, name string) (contextstate.SessionAdmission, error) {
	if err := principal.Validate(); err != nil {
		return contextstate.SessionAdmission{}, err
	}
	if err := validateSessionCatalogName(name); err != nil {
		return contextstate.SessionAdmission{}, err
	}
	if err := rejectManagedCatalogKey(ctx, s.db, principal, name); err != nil {
		return contextstate.SessionAdmission{}, err
	}
	var record contextstate.SessionAdmission
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT agent,digest,names FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND name=? AND instance_id IS NULL`, principal.WorkspaceID, principal.SubjectID, name).Scan(&record.Agent, &record.Digest, &encoded)
	if err == sql.ErrNoRows {
		return contextstate.SessionAdmission{}, nil
	}
	if err != nil {
		return contextstate.SessionAdmission{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &record.Names); err != nil {
		return contextstate.SessionAdmission{}, fmt.Errorf("%w: decode admitted tools", contextstate.ErrInvalidDTO)
	}
	return record, nil
}
