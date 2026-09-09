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

// TestValidateMemoryIndexSchemaSurfacesDriverErrors drives
// validateMemoryIndexSchema against a fake driver that can fail at each of
// its four query points in turn: a PRAGMA table_info column scan, PRAGMA
// table_info row iteration, the index-presence count query, and the
// CHECK-constraint sql text query. Each subtest isolates exactly one
// failure point so the corresponding error-propagation branch is the one
// that actually fires, not an earlier one.
func TestValidateMemoryIndexSchemaSurfacesDriverErrors(t *testing.T) {
	registerV16ErrorDriver.Do(func() {
		sql.Register("mivia-v16-schema-error", v16ErrorDriver{})
	})

	cases := []struct {
		name string
		mode string
	}{
		{"table_info column scan fails", "table_info_scan"},
		{"table_info row iteration fails", "table_info_rows_err"},
		{"index count query fails", "index_count_err"},
		{"check constraint sql query fails", "sql_text_err"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("mivia-v16-schema-error", tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := validateMemoryIndexSchema(db); err == nil {
				t.Fatalf("validateMemoryIndexSchema hid the %s failure", tc.mode)
			}
		})
	}
}

var registerV16ErrorDriver sync.Once

// v16ErrorDriver is a database/sql/driver fake whose Open name selects which
// of validateMemoryIndexSchema's four query points fails. It routes purely
// on the literal query text since validateMemoryIndexSchema issues distinct,
// recognizable SQL for each step.
type v16ErrorDriver struct{}

func (v16ErrorDriver) Open(name string) (driver.Conn, error) {
	return &v16ErrorConn{mode: name}, nil
}

type v16ErrorConn struct{ mode string }

func (c *v16ErrorConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *v16ErrorConn) Close() error { return nil }
func (c *v16ErrorConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *v16ErrorConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "PRAGMA table_info"):
		return &v16TableInfoRows{mode: c.mode}, nil
	case strings.Contains(query, "type='index'"):
		if c.mode == "index_count_err" {
			return nil, errors.New("index count query failed")
		}
		return &v16SingleValueRows{column: "count", value: int64(1)}, nil
	case strings.Contains(query, "type='table'"):
		if c.mode == "sql_text_err" {
			return nil, errors.New("sql text query failed")
		}
		return &v16SingleValueRows{column: "sql", value: v16AllConstraintsSQL}, nil
	default:
		return nil, errors.New("unexpected query in v16ErrorConn: " + query)
	}
}

// v16AllConstraintsSQL contains every CHECK-clause substring
// validateMemoryIndexSchema looks for across both memory_sources and
// memory_entries, so the fake "sql text" query point passes regardless of
// which table is currently being validated.
const v16AllConstraintsSQL = `CHECK((scope='project' AND project_id <> '' AND org_id='') OR ...) ` +
	`CHECK(source_path <> '') CHECK(source_hash <> '') CHECK(id <> '') ` +
	`CHECK(verdict IN ('good','bad','mixed','neutral')) CHECK(tier IN ('core','archive'))`

// v16TableInfoColumns is the full union of columns validateMemoryIndexSchema
// requires across memory_sources and memory_entries, so a normal
// table_info response satisfies both tables' checks.
var v16TableInfoColumns = []string{
	"id", "scope", "project_id", "org_id", "source_path", "source_hash",
	"title", "summary", "verdict", "tags", "created", "content", "tier", "indexed_at",
}

// v16TableInfoRows fakes a PRAGMA table_info(...) result set. In mode
// "table_info_scan" its first row carries a non-numeric "cid" value so
// rows.Scan fails. In mode "table_info_rows_err" it yields one valid row
// then fails on the next Next() call, simulating a mid-iteration driver
// error surfaced through rows.Err(). Any other mode returns the full,
// well-formed column set.
type v16TableInfoRows struct {
	mode string
	idx  int
}

func (r *v16TableInfoRows) Columns() []string {
	return []string{"cid", "name", "type", "notnull", "dflt_value", "pk"}
}
func (r *v16TableInfoRows) Close() error { return nil }
func (r *v16TableInfoRows) Next(values []driver.Value) error {
	if r.mode == "table_info_scan" && r.idx == 0 {
		r.idx++
		// "not-a-number" cannot convert into the *int cid destination,
		// so rows.Scan itself returns an error.
		copy(values, []driver.Value{"not-a-number", "id", "TEXT", int64(1), nil, int64(1)})
		return nil
	}
	if r.mode == "table_info_rows_err" && r.idx >= 1 {
		return errors.New("row iteration failed")
	}
	if r.idx >= len(v16TableInfoColumns) {
		return io.EOF
	}
	name := v16TableInfoColumns[r.idx]
	r.idx++
	copy(values, []driver.Value{int64(r.idx), name, "TEXT", int64(0), nil, int64(0)})
	return nil
}

// v16SingleValueRows fakes a single-column, single-row result, used for the
// index-presence count query and the CHECK-constraint sql text query.
type v16SingleValueRows struct {
	column string
	value  driver.Value
	sent   bool
}

func (r *v16SingleValueRows) Columns() []string { return []string{r.column} }
func (r *v16SingleValueRows) Close() error      { return nil }
func (r *v16SingleValueRows) Next(values []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	values[0] = r.value
	return nil
}
