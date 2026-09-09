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

// contentGCFault selects one point of failure in PruneOrphanedContent's
// two-query flow, exercised through a fake driver so the candidate-scan's
// rows.Err() branch and the live-scan's own query error can be pinned
// without a corruptible on-disk database.
type contentGCFault struct {
	// candidateRowsErr, when true, makes the candidate query ("FROM
	// content WHERE") yield one row then a non-EOF Next() error, which
	// surfaces as rows.Err() after the loop.
	candidateRowsErr bool
	// liveQueryErr, when true, makes the live-scan query ("FROM events")
	// fail outright.
	liveQueryErr bool
}

var contentGCFaultSeq int64

func openContentGCFaultDB(t *testing.T, fault contentGCFault) *SQLite {
	t.Helper()
	name := fmt.Sprintf("mivia-content-gc-fault-%d", atomic.AddInt64(&contentGCFaultSeq, 1))
	sql.Register(name, contentGCFaultDriver{fault: fault})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &SQLite{db: db}
}

type contentGCFaultDriver struct{ fault contentGCFault }

func (d contentGCFaultDriver) Open(string) (driver.Conn, error) {
	return &contentGCFaultConn{fault: d.fault}, nil
}

type contentGCFaultConn struct {
	fault        contentGCFault
	candidateHit bool
}

func (c *contentGCFaultConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *contentGCFaultConn) Close() error { return nil }
func (c *contentGCFaultConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *contentGCFaultConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "FROM content WHERE"):
		if c.fault.candidateRowsErr && !c.candidateHit {
			c.candidateHit = true
			return &contentGCOneRowThenErrRows{}, nil
		}
		if c.fault.liveQueryErr && !c.candidateHit {
			c.candidateHit = true
			return &contentGCOneRowRows{}, nil
		}
		return &contentGCEmptyRows{}, nil
	case strings.Contains(query, "FROM events"):
		if c.fault.liveQueryErr {
			return nil, errors.New("boom: events query failed")
		}
		return &contentGCEmptyRows{}, nil
	}
	return nil, fmt.Errorf("unexpected query in contentGCFaultConn: %s", query)
}

type contentGCEmptyRows struct{}

func (*contentGCEmptyRows) Columns() []string              { return []string{"ref"} }
func (*contentGCEmptyRows) Close() error                   { return nil }
func (*contentGCEmptyRows) Next(dest []driver.Value) error { return io.EOF }

// contentGCOneRowRows yields exactly one candidate ref, then a clean EOF.
type contentGCOneRowRows struct{ done bool }

func (*contentGCOneRowRows) Columns() []string { return []string{"ref"} }
func (*contentGCOneRowRows) Close() error      { return nil }
func (r *contentGCOneRowRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = "sha256:" + strings.Repeat("a", 64)
	return nil
}

// contentGCOneRowThenErrRows yields one candidate ref, then a non-EOF
// Next() error, which database/sql surfaces as rows.Err() after the loop.
type contentGCOneRowThenErrRows struct{ done bool }

func (*contentGCOneRowThenErrRows) Columns() []string { return []string{"ref"} }
func (*contentGCOneRowThenErrRows) Close() error      { return nil }
func (r *contentGCOneRowThenErrRows) Next(dest []driver.Value) error {
	if r.done {
		return errors.New("driver: connection reset mid-scan")
	}
	r.done = true
	dest[0] = "sha256:" + strings.Repeat("a", 64)
	return nil
}

// TestPruneOrphanedContent_CandidateRowsErrSurfaces pins the candidate
// scan's own rows.Err() branch.
func TestPruneOrphanedContent_CandidateRowsErrSurfaces(t *testing.T) {
	s := openContentGCFaultDB(t, contentGCFault{candidateRowsErr: true})
	if _, err := s.PruneOrphanedContent(context.Background()); err == nil {
		t.Fatal("PruneOrphanedContent hid a candidate-scan rows.Err()")
	}
}

// TestPruneOrphanedContent_LiveScanErrorSurfaces pins PruneOrphanedContent's
// own propagation of liveWorkflowContentRefs' error.
func TestPruneOrphanedContent_LiveScanErrorSurfaces(t *testing.T) {
	s := openContentGCFaultDB(t, contentGCFault{liveQueryErr: true})
	if _, err := s.PruneOrphanedContent(context.Background()); err == nil {
		t.Fatal("PruneOrphanedContent hid a live-scan query failure")
	}
}
