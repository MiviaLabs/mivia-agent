package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrAutomationRunNotFound is returned by UpdateAutomationRunState when no
// row matches the given id - distinguishing "nothing to update" from a
// genuine SQL error, mirroring ErrClaimNotHeld's role for run_claims.
var ErrAutomationRunNotFound = errors.New("automation run not found")

// AutomationRun is one row of the automation_runs table
// (internal/automation's run lifecycle; "Run History Schema" in
// docs/design/automations.md).
// Columns match migrateAutomationRunsSchema (automation_schema.go) exactly.
// Timestamps are RFC3339 strings, matching this store's other TEXT-timestamp
// columns (e.g. run_claims.acquired_at); EndedAt is nil until the run
// reaches a terminal state. internal/automation owns ID/AutomationID charset
// validation ("Identification Rules") and the state-name vocabulary; this type and its methods
// perform no automation-domain validation, only the SQL round trip.
type AutomationRun struct {
	ID             string
	AutomationID   string
	Origin         string
	State          string
	StepIndex      int
	StepCount      int
	SessionName    string
	WorktreePath   string
	WorktreeBranch string
	ClaimToken     string
	StartedAt      string
	EndedAt        *string
	FailKind       string
	Message        string
}

const automationRunColumns = `id, automation_id, origin, state, step_index, step_count, session_name, worktree_path, worktree_branch, claim_token, started_at, ended_at, fail_kind, message`

// InsertAutomationRun inserts one run row. A duplicate id returns
// ErrDuplicate, matching this package's other insert paths.
func (s *SQLite) InsertAutomationRun(ctx context.Context, r AutomationRun) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO automation_runs (`+automationRunColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.AutomationID, r.Origin, r.State, r.StepIndex, r.StepCount, r.SessionName, r.WorktreePath, r.WorktreeBranch, r.ClaimToken, r.StartedAt, r.EndedAt, r.FailKind, r.Message)
	if err != nil {
		if isConstraint(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("insert automation run %q: %w", r.ID, err)
	}
	return nil
}

// UpdateAutomationRunState is the single writer for every run-lifecycle
// transition: state, step_index, ended_at, fail_kind, and message all move
// together in one UPDATE. Returns ErrAutomationRunNotFound when id has no
// row.
func (s *SQLite) UpdateAutomationRunState(ctx context.Context, id, state string, stepIndex int, endedAt *string, failKind, message string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE automation_runs SET state=?, step_index=?, ended_at=?, fail_kind=?, message=? WHERE id=?`,
		state, stepIndex, endedAt, failKind, message, id)
	if err != nil {
		return fmt.Errorf("update automation run %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update automation run %q: %w", id, err)
	}
	if n == 0 {
		return ErrAutomationRunNotFound
	}
	return nil
}

// UpdateAutomationRunStateFenced writes the same lifecycle columns as
// UpdateAutomationRunState, but only when the row's claim_token still
// equals claimToken. It returns false, nil when no row matched: the
// caller lost its claim (a resume rotated the token) or the id is
// unknown. Timestamps are RFC3339 strings, like every other column here.
func (s *SQLite) UpdateAutomationRunStateFenced(ctx context.Context, id, claimToken, state string, stepIndex int, endedAt *string, failKind, message string) (bool, error) {
	return s.execAutomationRunUpdate(ctx, id, `UPDATE automation_runs SET state=?, step_index=?, ended_at=?, fail_kind=?, message=? WHERE id=? AND claim_token=?`,
		state, stepIndex, endedAt, failKind, message, id, claimToken)
}

// UpdateAutomationRunStateFencedIfRunning writes the same lifecycle
// columns as UpdateAutomationRunStateFenced, but only while the row's
// claim_token still equals claimToken AND its state is still 'running'.
// It exists for the executor's mid-run checkpoint: mirroring
// InterruptRunningAutomationRun's own "WHERE state='running'" guard
// closes the race where that conditional interrupt can land on a row
// between a step's completion and its checkpoint write - without this
// added state guard, the checkpoint's claim-token-only fence could
// overwrite an already-interrupted row back to state='running' with a
// later step_index, resurrecting a row crash recovery already closed
// out. It returns false, nil when no row matched: the caller lost its
// claim, the id is unknown, or - the case this method exists for - the
// row already moved off running.
func (s *SQLite) UpdateAutomationRunStateFencedIfRunning(ctx context.Context, id, claimToken, state string, stepIndex int, endedAt *string, failKind, message string) (bool, error) {
	return s.execAutomationRunUpdate(ctx, id, `UPDATE automation_runs SET state=?, step_index=?, ended_at=?, fail_kind=?, message=? WHERE id=? AND claim_token=? AND state='running'`,
		state, stepIndex, endedAt, failKind, message, id, claimToken)
}

// InterruptRunningAutomationRun marks a row interrupted only while it is
// still in state running, and keeps its step_index, so a run that
// finished or checkpointed between a read and this write is never
// overwritten. It returns false, nil when no row matched.
func (s *SQLite) InterruptRunningAutomationRun(ctx context.Context, id, endedAt, message string) (bool, error) {
	return s.execAutomationRunUpdate(ctx, id, `UPDATE automation_runs SET state='interrupted', ended_at=?, fail_kind='', message=? WHERE id=? AND state='running'`,
		endedAt, message, id)
}

// execAutomationRunUpdate runs one conditional UPDATE and reports
// whether a row matched.
func (s *SQLite) execAutomationRunUpdate(ctx context.Context, id, query string, args ...any) (bool, error) {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("update automation run %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update automation run %q: %w", id, err)
	}
	return n > 0, nil
}

// UpdateAutomationRunSession persists sessionName onto an existing run
// row - a follow-up write to a row already created by InsertAutomationRun,
// needed because the session name is only known after the run's session
// is created, not at insert time. Returns ErrAutomationRunNotFound when
// id has no row.
func (s *SQLite) UpdateAutomationRunSession(ctx context.Context, id, sessionName string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE automation_runs SET session_name=? WHERE id=?`,
		sessionName, id)
	if err != nil {
		return fmt.Errorf("update automation run %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update automation run %q: %w", id, err)
	}
	if n == 0 {
		return ErrAutomationRunNotFound
	}
	return nil
}

// UpdateAutomationRunClaimToken persists claimToken onto an existing run
// row - a follow-up write to a row already created by InsertAutomationRun,
// needed because the claim token is only known after the run's claim is
// acquired, not at insert time. Returns ErrAutomationRunNotFound when id
// has no row.
func (s *SQLite) UpdateAutomationRunClaimToken(ctx context.Context, id, claimToken string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE automation_runs SET claim_token=? WHERE id=?`,
		claimToken, id)
	if err != nil {
		return fmt.Errorf("update automation run %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update automation run %q: %w", id, err)
	}
	if n == 0 {
		return ErrAutomationRunNotFound
	}
	return nil
}

// GetAutomationRun reads one run by id. A missing row is reported as
// (AutomationRun{}, false, nil), not an error.
func (s *SQLite) GetAutomationRun(ctx context.Context, id string) (AutomationRun, bool, error) {
	var r AutomationRun
	err := s.db.QueryRowContext(ctx, `SELECT `+automationRunColumns+` FROM automation_runs WHERE id = ?`, id).Scan(
		&r.ID, &r.AutomationID, &r.Origin, &r.State, &r.StepIndex, &r.StepCount, &r.SessionName, &r.WorktreePath, &r.WorktreeBranch, &r.ClaimToken, &r.StartedAt, &r.EndedAt, &r.FailKind, &r.Message)
	if err == sql.ErrNoRows {
		return AutomationRun{}, false, nil
	}
	if err != nil {
		return AutomationRun{}, false, fmt.Errorf("get automation run %q: %w", id, err)
	}
	return r, true, nil
}

// ListAutomationRuns reads every run for automationID, most-recently
// started first. limit <= 0 means unlimited.
func (s *SQLite) ListAutomationRuns(ctx context.Context, automationID string, limit int) ([]AutomationRun, error) {
	query := `SELECT ` + automationRunColumns + ` FROM automation_runs WHERE automation_id = ? ORDER BY started_at DESC, id DESC`
	args := []any{automationID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list automation runs %q: %w", automationID, err)
	}
	defer rows.Close()
	return scanAutomationRuns(rows)
}

// ListRunningAutomationRuns reads every run currently in state "running",
// across all automations - the crash-recovery sweep's input set
// ("Interrupted Run Sweep").
func (s *SQLite) ListRunningAutomationRuns(ctx context.Context) ([]AutomationRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+automationRunColumns+` FROM automation_runs WHERE state = 'running' ORDER BY started_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list running automation runs: %w", err)
	}
	defer rows.Close()
	return scanAutomationRuns(rows)
}

// GetRunningAutomationRunByClaimToken reads the one row (if any) that is
// state='running', belongs to automationID, and carries claimToken as
// its claim_token - the exact-association lookup a lazy stale-claim
// takeover (admitFire, claim.go) needs to find and close out the prior
// holder's own orphaned run without touching a concurrent, genuinely
// fresh run for the SAME automation (which would carry a different
// claim_token) or any run belonging to a different automation. A
// missing row is reported as (AutomationRun{}, false, nil), matching
// GetAutomationRun's own "not found is not an error" contract - the
// caller (interruptOrphanedRun) is expected to treat "nothing to
// interrupt" as a normal, common outcome, not a store failure.
func (s *SQLite) GetRunningAutomationRunByClaimToken(ctx context.Context, automationID, claimToken string) (AutomationRun, bool, error) {
	var r AutomationRun
	err := s.db.QueryRowContext(ctx, `SELECT `+automationRunColumns+` FROM automation_runs WHERE automation_id = ? AND claim_token = ? AND state = 'running'`, automationID, claimToken).Scan(
		&r.ID, &r.AutomationID, &r.Origin, &r.State, &r.StepIndex, &r.StepCount, &r.SessionName, &r.WorktreePath, &r.WorktreeBranch, &r.ClaimToken, &r.StartedAt, &r.EndedAt, &r.FailKind, &r.Message)
	if err == sql.ErrNoRows {
		return AutomationRun{}, false, nil
	}
	if err != nil {
		return AutomationRun{}, false, fmt.Errorf("get running automation run by claim token %q: %w", automationID, err)
	}
	return r, true, nil
}

func scanAutomationRuns(rows *sql.Rows) ([]AutomationRun, error) {
	var out []AutomationRun
	for rows.Next() {
		var r AutomationRun
		if err := rows.Scan(&r.ID, &r.AutomationID, &r.Origin, &r.State, &r.StepIndex, &r.StepCount, &r.SessionName, &r.WorktreePath, &r.WorktreeBranch, &r.ClaimToken, &r.StartedAt, &r.EndedAt, &r.FailKind, &r.Message); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
