package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

// memIndexSyncFault configures exactly one point of failure in a
// syncMemoryIndexOnce transaction, exercised through a fake driver so the
// real statement-error and Scan/Err-propagation branches in
// loadMemoryIndexScopeState, the old-path delete loop, and
// upsertMemoryIndexDocument can be pinned without a corruptible on-disk
// database.
type memIndexSyncFault struct {
	// execFailSubstr, when non-empty, makes the first ExecContext whose SQL
	// contains this substring return execErr instead of succeeding.
	execFailSubstr string
	execErr        error

	// scanFailSubstr, when non-empty, makes the QueryContext matching this
	// substring hand back a row that fails Scan (a source_hash column typed
	// as int64, which cannot convert into the *string destination).
	scanFailSubstr string

	// rowsErrFailSubstr, when non-empty, makes the QueryContext matching
	// this substring hand back zero rows but a non-nil, non-EOF Next()
	// error, which surfaces as rows.Err() after the loop.
	rowsErrFailSubstr string

	// badRowsAffected, when true, makes the UPDATE memory_entries exec
	// return a Result whose RowsAffected() itself errors.
	badRowsAffected bool

	// sourceRow, when set, is the single row served for the
	// "FROM memory_sources WHERE" query (unless scan/rows-err faulted).
	sourceRow *[2]string
}

var memIndexSyncFaultDriverSeq int64

func openMemIndexSyncFaultDB(t *testing.T, fault memIndexSyncFault) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("mivia-memindex-sync-fault-%d", atomic.AddInt64(&memIndexSyncFaultDriverSeq, 1))
	sql.Register(name, memIndexSyncFaultDriver{fault: fault})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type memIndexSyncFaultDriver struct{ fault memIndexSyncFault }

func (d memIndexSyncFaultDriver) Open(string) (driver.Conn, error) {
	return &memIndexSyncFaultConn{fault: d.fault}, nil
}

type memIndexSyncFaultConn struct {
	fault       memIndexSyncFault
	execDone    bool
	scanDone    bool
	rowsErrDone bool
}

func (c *memIndexSyncFaultConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *memIndexSyncFaultConn) Close() error { return nil }
func (c *memIndexSyncFaultConn) Begin() (driver.Tx, error) {
	return memIndexSyncFaultTx{}, nil
}

type memIndexSyncFaultTx struct{}

func (memIndexSyncFaultTx) Commit() error   { return nil }
func (memIndexSyncFaultTx) Rollback() error { return nil }

func (c *memIndexSyncFaultConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if c.fault.execFailSubstr != "" && !c.execDone && strings.Contains(query, c.fault.execFailSubstr) {
		c.execDone = true
		return nil, c.fault.execErr
	}
	if c.fault.badRowsAffected && strings.HasPrefix(query, "UPDATE memory_entries SET") {
		return memIndexBadResult{}, nil
	}
	return driver.RowsAffected(0), nil
}

func (c *memIndexSyncFaultConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.fault.scanFailSubstr != "" && !c.scanDone && strings.Contains(query, c.fault.scanFailSubstr) {
		c.scanDone = true
		return &memIndexBadScanRows{}, nil
	}
	if c.fault.rowsErrFailSubstr != "" && !c.rowsErrDone && strings.Contains(query, c.fault.rowsErrFailSubstr) {
		c.rowsErrDone = true
		return &memIndexRowsErrRows{}, nil
	}
	if strings.Contains(query, "FROM memory_sources WHERE") && c.fault.sourceRow != nil {
		return &memIndexSourceRows{row: *c.fault.sourceRow}, nil
	}
	return &memIndexEmptyRows{}, nil
}

// memIndexBadResult's RowsAffected() errors, pinning upsertMemoryIndexDocument's
// own error wrap around that call.
type memIndexBadResult struct{}

func (memIndexBadResult) LastInsertId() (int64, error) { return 0, nil }
func (memIndexBadResult) RowsAffected() (int64, error) {
	return 0, errors.New("driver: rows affected is unavailable")
}

// memIndexEmptyRows yields zero rows and a clean EOF.
type memIndexEmptyRows struct{}

func (*memIndexEmptyRows) Columns() []string              { return []string{"a", "b"} }
func (*memIndexEmptyRows) Close() error                   { return nil }
func (*memIndexEmptyRows) Next(dest []driver.Value) error { return io.EOF }

// memIndexSourceRows yields one (source_path, source_hash) row.
type memIndexSourceRows struct {
	row  [2]string
	done bool
}

func (*memIndexSourceRows) Columns() []string { return []string{"source_path", "source_hash"} }
func (*memIndexSourceRows) Close() error      { return nil }
func (r *memIndexSourceRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.row[0]
	dest[1] = r.row[1]
	return nil
}

// memIndexBadScanRows yields one row whose second column is an int64,
// which cannot convert into the *string destination rows.Scan expects -
// forcing the Scan-error branch immediately after the loop's first
// iteration.
type memIndexBadScanRows struct{ done bool }

func (*memIndexBadScanRows) Columns() []string { return []string{"a", "b"} }
func (*memIndexBadScanRows) Close() error      { return nil }
func (r *memIndexBadScanRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = "p1"
	dest[1] = struct{}{} // unconvertible: forces rows.Scan to fail
	return nil
}

// memIndexRowsErrRows yields zero rows but a non-EOF Next() error, which
// database/sql surfaces as rows.Err() once the loop exits.
type memIndexRowsErrRows struct{}

func (*memIndexRowsErrRows) Columns() []string { return []string{"a", "b"} }
func (*memIndexRowsErrRows) Close() error      { return nil }
func (*memIndexRowsErrRows) Next(dest []driver.Value) error {
	return errors.New("driver: connection reset mid-scan")
}

func newFaultedSQLite(db *sql.DB) *SQLite { return &SQLite{db: db} }

// TestSyncMemoryIndex_OldPathDeleteEntriesExecError pins the memory_entries
// delete's own error wrap in the old-path removal loop.
func TestSyncMemoryIndex_OldPathDeleteEntriesExecError(t *testing.T) {
	row := [2]string{"gone.md", "hash1"}
	db := openMemIndexSyncFaultDB(t, memIndexSyncFault{
		execFailSubstr: "DELETE FROM memory_entries WHERE scope=? AND project_id=? AND org_id=? AND source_path=?",
		execErr:        errors.New("boom: delete entries failed"),
		sourceRow:      &row,
	})
	s := newFaultedSQLite(db)
	err := s.syncMemoryIndexOnce(context.Background(), "project", "repo", "", nil)
	if err == nil || !strings.Contains(err.Error(), "boom: delete entries failed") {
		t.Fatalf("syncMemoryIndexOnce() error = %v, want the delete-entries error", err)
	}
}

// TestSyncMemoryIndex_OldPathDeleteSourcesExecError pins the memory_sources
// delete's own error wrap, distinct from the entries delete above.
func TestSyncMemoryIndex_OldPathDeleteSourcesExecError(t *testing.T) {
	row := [2]string{"gone.md", "hash1"}
	db := openMemIndexSyncFaultDB(t, memIndexSyncFault{
		execFailSubstr: "DELETE FROM memory_sources WHERE scope=? AND project_id=? AND org_id=? AND source_path=?",
		execErr:        errors.New("boom: delete sources failed"),
		sourceRow:      &row,
	})
	s := newFaultedSQLite(db)
	err := s.syncMemoryIndexOnce(context.Background(), "project", "repo", "", nil)
	if err == nil || !strings.Contains(err.Error(), "boom: delete sources failed") {
		t.Fatalf("syncMemoryIndexOnce() error = %v, want the delete-sources error", err)
	}
}

// TestLoadMemoryIndexScopeState_SourceRowsScanError pins the source-rows
// Scan-error branch.
func TestLoadMemoryIndexScopeState_SourceRowsScanError(t *testing.T) {
	db := openMemIndexSyncFaultDB(t, memIndexSyncFault{
		scanFailSubstr: "FROM memory_sources WHERE",
	})
	s := newFaultedSQLite(db)
	err := s.syncMemoryIndexOnce(context.Background(), "project", "repo", "", nil)
	if err == nil {
		t.Fatal("syncMemoryIndexOnce hid a memory_sources row-scan type mismatch")
	}
}

// TestLoadMemoryIndexScopeState_SourceRowsErr pins the rows.Err() branch
// after the memory_sources scan loop.
func TestLoadMemoryIndexScopeState_SourceRowsErr(t *testing.T) {
	db := openMemIndexSyncFaultDB(t, memIndexSyncFault{
		rowsErrFailSubstr: "FROM memory_sources WHERE",
	})
	s := newFaultedSQLite(db)
	err := s.syncMemoryIndexOnce(context.Background(), "project", "repo", "", nil)
	if err == nil || !strings.Contains(err.Error(), "connection reset mid-scan") {
		t.Fatalf("syncMemoryIndexOnce() error = %v, want the rows.Err() propagated", err)
	}
}

// TestLoadMemoryIndexScopeState_EntryRowsScanError pins the memory_entries
// row-scan error branch, distinct from the memory_sources one above.
func TestLoadMemoryIndexScopeState_EntryRowsScanError(t *testing.T) {
	db := openMemIndexSyncFaultDB(t, memIndexSyncFault{
		scanFailSubstr: "FROM memory_entries WHERE",
	})
	s := newFaultedSQLite(db)
	err := s.syncMemoryIndexOnce(context.Background(), "project", "repo", "", nil)
	if err == nil {
		t.Fatal("syncMemoryIndexOnce hid a memory_entries row-scan type mismatch")
	}
}

// TestLoadMemoryIndexScopeState_EntryRowsErr pins the rows.Err() branch
// after the memory_entries scan loop.
func TestLoadMemoryIndexScopeState_EntryRowsErr(t *testing.T) {
	db := openMemIndexSyncFaultDB(t, memIndexSyncFault{
		rowsErrFailSubstr: "FROM memory_entries WHERE",
	})
	s := newFaultedSQLite(db)
	err := s.syncMemoryIndexOnce(context.Background(), "project", "repo", "", nil)
	if err == nil || !strings.Contains(err.Error(), "connection reset mid-scan") {
		t.Fatalf("syncMemoryIndexOnce() error = %v, want the rows.Err() propagated", err)
	}
}

// TestUpsertMemoryIndexDocument_DeleteEntriesExecError pins the upsert's own
// DELETE (...AND id<>?) error wrap, distinct from the old-path delete above
// by its extra "id<>?" clause.
func TestUpsertMemoryIndexDocument_DeleteEntriesExecError(t *testing.T) {
	db := openMemIndexSyncFaultDB(t, memIndexSyncFault{
		execFailSubstr: "DELETE FROM memory_entries WHERE scope=? AND project_id=? AND org_id=? AND source_path=? AND id<>?",
		execErr:        errors.New("boom: upsert delete failed"),
	})
	s := newFaultedSQLite(db)
	doc := MemoryIndexDocument{ID: "d1", Scope: "project", ProjectID: "repo", SourcePath: "p.md", SourceHash: "h1"}
	err := s.syncMemoryIndexOnce(context.Background(), "project", "repo", "", []MemoryIndexDocument{doc})
	if err == nil || !strings.Contains(err.Error(), "boom: upsert delete failed") {
		t.Fatalf("syncMemoryIndexOnce() error = %v, want the upsert delete error", err)
	}
}

// TestUpsertMemoryIndexDocument_InsertSourcesExecError pins the
// INSERT INTO memory_sources error wrap.
func TestUpsertMemoryIndexDocument_InsertSourcesExecError(t *testing.T) {
	db := openMemIndexSyncFaultDB(t, memIndexSyncFault{
		execFailSubstr: "INSERT INTO memory_sources",
		execErr:        errors.New("boom: insert sources failed"),
	})
	s := newFaultedSQLite(db)
	doc := MemoryIndexDocument{ID: "d1", Scope: "project", ProjectID: "repo", SourcePath: "p.md", SourceHash: "h1"}
	err := s.syncMemoryIndexOnce(context.Background(), "project", "repo", "", []MemoryIndexDocument{doc})
	if err == nil || !strings.Contains(err.Error(), "boom: insert sources failed") {
		t.Fatalf("syncMemoryIndexOnce() error = %v, want the insert-sources error", err)
	}
}

// TestUpsertMemoryIndexDocument_RowsAffectedError pins the
// result.RowsAffected() error wrap after the UPDATE memory_entries exec.
func TestUpsertMemoryIndexDocument_RowsAffectedError(t *testing.T) {
	db := openMemIndexSyncFaultDB(t, memIndexSyncFault{badRowsAffected: true})
	s := newFaultedSQLite(db)
	doc := MemoryIndexDocument{ID: "d1", Scope: "project", ProjectID: "repo", SourcePath: "p.md", SourceHash: "h1"}
	err := s.syncMemoryIndexOnce(context.Background(), "project", "repo", "", []MemoryIndexDocument{doc})
	if err == nil || !strings.Contains(err.Error(), "rows affected is unavailable") {
		t.Fatalf("syncMemoryIndexOnce() error = %v, want the RowsAffected error", err)
	}
}
