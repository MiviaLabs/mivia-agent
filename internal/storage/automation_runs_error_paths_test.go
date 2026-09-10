package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestInsertAutomationRunDuplicateReturnsErrDuplicate covers
// InsertAutomationRun's isConstraint branch for real: inserting the same
// id twice against a genuine SQLite database triggers the table's real
// PRIMARY KEY constraint, exercising the ErrDuplicate translation without
// any fake driver.
func TestInsertAutomationRunDuplicateReturnsErrDuplicate(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "dup-run.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	row := AutomationRun{ID: "dup-run", AutomationID: "auto-1", Origin: "manual", State: "pending", StartedAt: "2026-09-10T00:00:00Z"}
	if err := s.InsertAutomationRun(context.Background(), row); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := s.InsertAutomationRun(context.Background(), row); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second insert (duplicate id) = %v, want ErrDuplicate", err)
	}
}

// TestUpdateAutomationRunStateNoMatchingRowReturnsErrAutomationRunNotFound
// covers UpdateAutomationRunState's n==0 branch for real: updating an id
// that was never inserted affects zero rows.
func TestUpdateAutomationRunStateNoMatchingRowReturnsErrAutomationRunNotFound(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "missing-run.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.UpdateAutomationRunState(context.Background(), "no-such-run", "failed", 0, nil, "job_error", "x"); !errors.Is(err, ErrAutomationRunNotFound) {
		t.Fatalf("UpdateAutomationRunState(missing id) = %v, want ErrAutomationRunNotFound", err)
	}
}

var registerAutomationRunsErrorDriver sync.Once

// automationRunsErrorDriver is a database/sql/driver fake covering the
// error-wrap branches of InsertAutomationRun, UpdateAutomationRunState,
// GetAutomationRun, ListAutomationRuns, ListRunningAutomationRuns, and
// scanAutomationRuns that a real SQLite database cannot reach
// deterministically (a raw ExecContext/QueryContext failure that is NOT a
// constraint violation, a RowsAffected failure, a row-scan type
// mismatch). The Open name selects which single failure point is active;
// mirrors internal/storage/memory_index_tier_error_test.go's
// tierErrorDriver shape exactly.
type automationRunsErrorDriver struct{}

func (automationRunsErrorDriver) Open(name string) (driver.Conn, error) {
	return &automationRunsErrorConn{mode: name}, nil
}

type automationRunsErrorConn struct{ mode string }

func (c *automationRunsErrorConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *automationRunsErrorConn) Close() error              { return nil }
func (c *automationRunsErrorConn) Begin() (driver.Tx, error) { return automationRunsErrorTx{}, nil }

func (c *automationRunsErrorConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	switch {
	case strings.Contains(query, "INSERT INTO automation_runs"):
		if c.mode == "insert_exec_err" {
			return nil, errors.New("insert exec failed")
		}
		return driver.RowsAffected(1), nil
	case strings.Contains(query, "UPDATE automation_runs"):
		if c.mode == "update_exec_err" {
			return nil, errors.New("update exec failed")
		}
		if c.mode == "update_rows_affected_err" {
			return automationRunsErrorResult{}, nil
		}
		return driver.RowsAffected(1), nil
	default:
		return nil, errors.New("unexpected exec in automationRunsErrorConn: " + query)
	}
}

func (c *automationRunsErrorConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "FROM automation_runs WHERE id = ?"):
		if c.mode == "get_query_err" {
			return nil, errors.New("get query failed")
		}
		return automationRunsErrorRow(), nil
	case strings.Contains(query, "WHERE automation_id = ?"):
		if c.mode == "list_exec_err" {
			return nil, errors.New("list query failed")
		}
		if c.mode == "list_scan_err" {
			return automationRunsErrorBadRow(), nil
		}
		return &automationRunsErrorEmptyRows{}, nil
	case strings.Contains(query, "WHERE state = 'running'"):
		if c.mode == "list_running_exec_err" {
			return nil, errors.New("list running query failed")
		}
		return &automationRunsErrorEmptyRows{}, nil
	default:
		return nil, errors.New("unexpected query in automationRunsErrorConn: " + query)
	}
}

func automationRunsErrorRow() driver.Rows {
	return &automationRunsErrorRowImpl{values: []driver.Value{
		"id", "auto", "manual", "running", int64(0), int64(0), "sess", "", "", "", "2026-09-10T00:00:00Z", nil, "", "",
	}}
}

// automationRunsErrorBadRow yields one row whose first column cannot
// convert into *string, exercising scanAutomationRuns' own Scan-error
// branch via ListAutomationRuns.
func automationRunsErrorBadRow() driver.Rows {
	return &automationRunsErrorRowImpl{values: []driver.Value{
		struct{ notAString int }{1}, "auto", "manual", "running", int64(0), int64(0), "sess", "", "", "", "2026-09-10T00:00:00Z", nil, "", "",
	}}
}

type automationRunsErrorTx struct{}

func (automationRunsErrorTx) Commit() error   { return nil }
func (automationRunsErrorTx) Rollback() error { return nil }

// automationRunsErrorResult's RowsAffected always errors, exercising
// UpdateAutomationRunState's "n, err := res.RowsAffected()" branch.
type automationRunsErrorResult struct{}

func (automationRunsErrorResult) LastInsertId() (int64, error) { return 0, nil }
func (automationRunsErrorResult) RowsAffected() (int64, error) {
	return 0, errors.New("rows affected unavailable")
}

// automationRunsErrorRowImpl yields exactly one row of the given values
// (all 14 automationRunColumns), then EOF.
type automationRunsErrorRowImpl struct {
	values []driver.Value
	sent   bool
}

func (r *automationRunsErrorRowImpl) Columns() []string {
	return []string{"id", "automation_id", "origin", "state", "step_index", "step_count", "session_name", "worktree_path", "worktree_branch", "claim_token", "started_at", "ended_at", "fail_kind", "message"}
}
func (r *automationRunsErrorRowImpl) Close() error { return nil }
func (r *automationRunsErrorRowImpl) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	copy(dest, r.values)
	return nil
}

// automationRunsErrorEmptyRows yields no rows at all (immediate EOF).
type automationRunsErrorEmptyRows struct{}

func (automationRunsErrorEmptyRows) Columns() []string              { return nil }
func (automationRunsErrorEmptyRows) Close() error                   { return nil }
func (automationRunsErrorEmptyRows) Next(dest []driver.Value) error { return io.EOF }

func newAutomationRunsErrorStore(t *testing.T, mode string) *SQLite {
	t.Helper()
	registerAutomationRunsErrorDriver.Do(func() {
		sql.Register("mivia-automation-runs-error", automationRunsErrorDriver{})
	})
	db, err := sql.Open("mivia-automation-runs-error", mode)
	if err != nil {
		t.Fatalf("open fault-injected db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &SQLite{db: db}
}

func TestInsertAutomationRunPropagatesNonConstraintExecError(t *testing.T) {
	store := newAutomationRunsErrorStore(t, "insert_exec_err")
	err := store.InsertAutomationRun(context.Background(), AutomationRun{ID: "x"})
	if err == nil || errors.Is(err, ErrDuplicate) {
		t.Fatalf("InsertAutomationRun with a non-constraint exec error = %v, want a wrapped raw error, not ErrDuplicate", err)
	}
}

func TestUpdateAutomationRunStatePropagatesExecError(t *testing.T) {
	store := newAutomationRunsErrorStore(t, "update_exec_err")
	if err := store.UpdateAutomationRunState(context.Background(), "x", "failed", 0, nil, "", ""); err == nil {
		t.Fatal("UpdateAutomationRunState hid an exec error")
	}
}

func TestUpdateAutomationRunStatePropagatesRowsAffectedError(t *testing.T) {
	store := newAutomationRunsErrorStore(t, "update_rows_affected_err")
	err := store.UpdateAutomationRunState(context.Background(), "x", "failed", 0, nil, "", "")
	if err == nil || errors.Is(err, ErrAutomationRunNotFound) {
		t.Fatalf("UpdateAutomationRunState with a RowsAffected error = %v, want the raw wrapped error, not ErrAutomationRunNotFound", err)
	}
}

func TestGetAutomationRunPropagatesNonNoRowsError(t *testing.T) {
	store := newAutomationRunsErrorStore(t, "get_query_err")
	_, _, err := store.GetAutomationRun(context.Background(), "x")
	if err == nil {
		t.Fatal("GetAutomationRun hid a real query error")
	}
}

func TestListAutomationRunsPropagatesQueryError(t *testing.T) {
	store := newAutomationRunsErrorStore(t, "list_exec_err")
	if _, err := store.ListAutomationRuns(context.Background(), "auto", 0); err == nil {
		t.Fatal("ListAutomationRuns hid a real query error")
	}
}

func TestListAutomationRunsPropagatesRowScanError(t *testing.T) {
	store := newAutomationRunsErrorStore(t, "list_scan_err")
	if _, err := store.ListAutomationRuns(context.Background(), "auto", 0); err == nil {
		t.Fatal("ListAutomationRuns hid a row-scan type mismatch (scanAutomationRuns' own error path)")
	}
}

func TestListRunningAutomationRunsPropagatesQueryError(t *testing.T) {
	store := newAutomationRunsErrorStore(t, "list_running_exec_err")
	if _, err := store.ListRunningAutomationRuns(context.Background()); err == nil {
		t.Fatal("ListRunningAutomationRuns hid a real query error")
	}
}

// TestAutomationRunsGoAPIRoundTrip exercises every remaining success-path
// line of InsertAutomationRun/UpdateAutomationRunState/GetAutomationRun/
// ListAutomationRuns/ListRunningAutomationRuns/scanAutomationRuns through
// the real Go methods, not raw SQL - TestAutomationRunsTableMigration
// above proves the SCHEMA round-trips at the SQL level, but never calls
// these methods themselves, leaving their own found/success branches
// (GetAutomationRun's true-found return, ListAutomationRuns' limit>0
// branch, ListRunningAutomationRuns finding a real row, and
// scanAutomationRuns' successful append+rows.Err() return) unexercised.
func TestAutomationRunsGoAPIRoundTrip(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "roundtrip.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	row := AutomationRun{ID: "run-rt", AutomationID: "auto-rt", Origin: "scheduled", State: "running", StartedAt: "2026-09-10T00:00:00Z"}
	if err := s.InsertAutomationRun(ctx, row); err != nil {
		t.Fatalf("InsertAutomationRun: %v", err)
	}
	if err := s.UpdateAutomationRunState(ctx, "run-rt", "running", 1, nil, "", ""); err != nil {
		t.Fatalf("UpdateAutomationRunState (real success): %v", err)
	}

	got, found, err := s.GetAutomationRun(ctx, "run-rt")
	if err != nil || !found {
		t.Fatalf("GetAutomationRun(found) = %+v, %v, %v", got, found, err)
	}
	if got.ID != "run-rt" || got.State != "running" {
		t.Fatalf("GetAutomationRun round-trip mismatch: %+v", got)
	}

	if _, found, err := s.GetAutomationRun(ctx, "no-such-run"); err != nil || found {
		t.Fatalf("GetAutomationRun(missing) = found=%v err=%v, want found=false err=nil", found, err)
	}

	listed, err := s.ListAutomationRuns(ctx, "auto-rt", 1)
	if err != nil {
		t.Fatalf("ListAutomationRuns(limit=1): %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "run-rt" {
		t.Fatalf("ListAutomationRuns(limit=1) = %+v, want one row for run-rt", listed)
	}

	running, err := s.ListRunningAutomationRuns(ctx)
	if err != nil {
		t.Fatalf("ListRunningAutomationRuns: %v", err)
	}
	found = false
	for _, r := range running {
		if r.ID == "run-rt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListRunningAutomationRuns did not return run-rt: %+v", running)
	}
}
