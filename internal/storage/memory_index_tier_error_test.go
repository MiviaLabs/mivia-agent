package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// TestPromoteMemoryIndexEntryPropagatesCoreCountQueryError drives
// PromoteMemoryIndexEntry against a fake driver that lets the initial
// "SELECT scope,tier" lookup succeed (tier="archive", so the early
// already-core return is skipped) but fails the following
// "SELECT COUNT(*)" core-tier-cap query, reaching the error-propagation
// branch right after it - distinct from the lookup-query failure already
// covered by TestPromoteMemoryIndexEntryPropagatesLookupQueryError.
func TestPromoteMemoryIndexEntryPropagatesCoreCountQueryError(t *testing.T) {
	registerTierErrorDriver.Do(func() {
		sql.Register("mivia-tier-error", tierErrorDriver{})
	})
	db, err := sql.Open("mivia-tier-error", "count_err")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &SQLite{db: db}
	err = store.PromoteMemoryIndexEntry(context.Background(), "one", "repo", "")
	if err == nil {
		t.Fatal("PromoteMemoryIndexEntry hid the core-count query error")
	}
	if errors.Is(err, ErrMemoryIndexEntryNotFound) {
		t.Fatalf("expected the raw count-query error, got the not-found sentinel: %v", err)
	}
}

// TestCoreMemoryIndexEntriesPropagatesRowScanError mirrors
// TestSearchMemoryIndexPropagatesRowScanError's technique for
// CoreMemoryIndexEntries' own row loop: the fake row's "id" column is a
// value type rows.Scan cannot convert into *string.
func TestCoreMemoryIndexEntriesPropagatesRowScanError(t *testing.T) {
	registerTierErrorDriver.Do(func() {
		sql.Register("mivia-tier-error", tierErrorDriver{})
	})
	db, err := sql.Open("mivia-tier-error", "scan_err")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &SQLite{db: db}
	if _, err := store.CoreMemoryIndexEntries(context.Background(), "project", "repo", ""); err == nil {
		t.Fatal("CoreMemoryIndexEntries hid a row-scan type mismatch")
	}
}

// TestDeleteMemoryIndexEntryPropagatesRowsAffectedError drives
// DeleteMemoryIndexEntry against a fake driver.Result whose RowsAffected()
// itself errors (distinct from the ExecContext failure already covered by
// TestDeleteMemoryIndexEntryPropagatesExecError, which fails one step
// earlier).
func TestDeleteMemoryIndexEntryPropagatesRowsAffectedError(t *testing.T) {
	registerTierErrorDriver.Do(func() {
		sql.Register("mivia-tier-error", tierErrorDriver{})
	})
	db, err := sql.Open("mivia-tier-error", "rows_affected_err")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &SQLite{db: db}
	if err := store.DeleteMemoryIndexEntry(context.Background(), "one", "repo", ""); err == nil {
		t.Fatal("DeleteMemoryIndexEntry hid a RowsAffected error")
	}
}

var registerTierErrorDriver sync.Once

// tierErrorDriver is a database/sql/driver fake covering
// PromoteMemoryIndexEntry, CoreMemoryIndexEntries, and
// DeleteMemoryIndexEntry. The Open name selects which single failure point
// is active; every other query/exec on the same connection succeeds with
// realistic data so the targeted branch - not an earlier one - is what
// fires.
type tierErrorDriver struct{}

func (tierErrorDriver) Open(name string) (driver.Conn, error) {
	return &tierErrorConn{mode: name}, nil
}

type tierErrorConn struct{ mode string }

func (c *tierErrorConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *tierErrorConn) Close() error              { return nil }
func (c *tierErrorConn) Begin() (driver.Tx, error) { return tierErrorTx{}, nil }

func (c *tierErrorConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "SELECT scope,tier"):
		return &tierErrorRow{columns: []string{"scope", "tier"}, values: []driver.Value{"project", "archive"}}, nil
	case strings.Contains(query, "SELECT COUNT(*) FROM memory_entries"):
		if c.mode == "count_err" {
			return nil, errors.New("core count query failed")
		}
		return &tierErrorRow{columns: []string{"count"}, values: []driver.Value{int64(0)}}, nil
	case strings.Contains(query, "FROM memory_entries WHERE scope=?"):
		if c.mode == "scan_err" {
			return &tierErrorRow{
				columns: []string{"id", "scope", "project_id", "org_id", "source_path", "source_hash", "title", "summary", "verdict", "tags", "created", "content", "tier"},
				values:  []driver.Value{struct{ notAString int }{1}, "x", "x", "x", "x", "x", "x", "x", "x", "x", "x", "x", "x"},
			}, nil
		}
		return &tierErrorEmptyRows{}, nil
	default:
		return nil, errors.New("unexpected query in tierErrorConn: " + query)
	}
}

func (c *tierErrorConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "UPDATE memory_entries") || strings.Contains(query, "DELETE FROM memory_entries") {
		if c.mode == "rows_affected_err" {
			return tierErrorResult{}, nil
		}
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("unexpected exec in tierErrorConn: " + query)
}

type tierErrorTx struct{}

func (tierErrorTx) Commit() error   { return nil }
func (tierErrorTx) Rollback() error { return nil }

// tierErrorResult's RowsAffected always errors, exercising
// DeleteMemoryIndexEntry's "count, err := result.RowsAffected()" branch.
type tierErrorResult struct{}

func (tierErrorResult) LastInsertId() (int64, error) { return 0, nil }
func (tierErrorResult) RowsAffected() (int64, error) {
	return 0, errors.New("rows affected unavailable")
}

// tierErrorRow yields exactly one row of the given columns/values, then EOF.
type tierErrorRow struct {
	columns []string
	values  []driver.Value
	sent    bool
}

func (r *tierErrorRow) Columns() []string { return r.columns }
func (r *tierErrorRow) Close() error      { return nil }
func (r *tierErrorRow) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	copy(dest, r.values)
	return nil
}

// tierErrorEmptyRows yields no rows at all (immediate EOF), used for the
// "not found" lookup path and CoreMemoryIndexEntries' non-scan-error modes.
type tierErrorEmptyRows struct{}

func (tierErrorEmptyRows) Columns() []string              { return nil }
func (tierErrorEmptyRows) Close() error                   { return nil }
func (tierErrorEmptyRows) Next(dest []driver.Value) error { return io.EOF }
