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

// TestSearchMemoryIndexPropagatesRowScanError drives SearchMemoryIndex
// against a fake driver whose single result row carries an int64 in the
// "id" column, which cannot convert into the *string destination
// rows.Scan expects - forcing the rows.Scan error-propagation branch (as
// opposed to the QueryContext error already covered by
// TestSearchMemoryIndexQueryContextError, which fails one step earlier).
func TestSearchMemoryIndexPropagatesRowScanError(t *testing.T) {
	registerSearchScanErrorDriver.Do(func() {
		sql.Register("mivia-search-scan-error", searchScanErrorDriver{})
	})
	db, err := sql.Open("mivia-search-scan-error", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &SQLite{db: db}
	if _, err := store.SearchMemoryIndex(context.Background(), "project", "repo", "", "cache", 8); err == nil {
		t.Fatal("SearchMemoryIndex hid a row-scan type mismatch")
	}
}

var registerSearchScanErrorDriver sync.Once

type searchScanErrorDriver struct{}

func (searchScanErrorDriver) Open(string) (driver.Conn, error) {
	return &searchScanErrorConn{}, nil
}

type searchScanErrorConn struct{}

func (c *searchScanErrorConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *searchScanErrorConn) Close() error { return nil }
func (c *searchScanErrorConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *searchScanErrorConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "FROM memory_entries WHERE") {
		return nil, errors.New("unexpected query in searchScanErrorConn: " + query)
	}
	return &searchScanErrorRows{}, nil
}

// searchScanErrorRows yields exactly one row whose first ("id") column is an
// int64, matching the 13-column SELECT list in SearchMemoryIndex
// (id,scope,project_id,org_id,source_path,source_hash,title,summary,verdict,tags,created,content,tier).
type searchScanErrorRows struct{ sent bool }

func (r *searchScanErrorRows) Columns() []string {
	return []string{"id", "scope", "project_id", "org_id", "source_path", "source_hash", "title", "summary", "verdict", "tags", "created", "content", "tier"}
}
func (r *searchScanErrorRows) Close() error { return nil }
func (r *searchScanErrorRows) Next(values []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	// database/sql's convertAssign happily stringifies numeric/bool
	// driver.Values into a *string destination, so a struct is used here -
	// its reflect.Kind falls through every conversion case and reaches the
	// "unsupported Scan" error path.
	values[0] = struct{ notAString int }{1}
	for i := 1; i < len(values); i++ {
		values[i] = "x"
	}
	return nil
}
