package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
)

var registerSchemaErrorDriver sync.Once

func TestSchemaInspectionRowErrors(t *testing.T) {
	registerSchemaErrorDriver.Do(func() {
		sql.Register("mivia-schema-error", schemaErrorDriver{})
	})
	t.Run("column scan", func(t *testing.T) {
		tx := schemaErrorTx(t, "scan")
		defer tx.Rollback()
		if _, _, err := contextInstanceColumnContract(tx, "table"); err == nil {
			t.Fatal("column scan error was hidden")
		}
	})
	t.Run("route rows", func(t *testing.T) {
		tx := schemaErrorTx(t, "rows")
		defer tx.Rollback()
		if _, err := worktreeRoutesV9Ready(tx); err == nil {
			t.Fatal("route row error was hidden")
		}
	})
}

func schemaErrorTx(t *testing.T, mode string) *sql.Tx {
	t.Helper()
	db, err := sql.Open("mivia-schema-error", mode)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

type schemaErrorDriver struct{}

func (schemaErrorDriver) Open(name string) (driver.Conn, error) {
	return &schemaErrorConn{mode: name}, nil
}

type schemaErrorConn struct{ mode string }

func (c *schemaErrorConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *schemaErrorConn) Close() error              { return nil }
func (c *schemaErrorConn) Begin() (driver.Tx, error) { return schemaErrorTxDriver{}, nil }
func (c *schemaErrorConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &schemaErrorRows{mode: c.mode}, nil
}

type schemaErrorTxDriver struct{}

func (schemaErrorTxDriver) Commit() error   { return nil }
func (schemaErrorTxDriver) Rollback() error { return nil }

type schemaErrorRows struct {
	mode string
	sent bool
}

func (r *schemaErrorRows) Columns() []string {
	return []string{"cid", "name", "type", "notnull", "default", "pk"}
}
func (r *schemaErrorRows) Close() error { return nil }
func (r *schemaErrorRows) Next(values []driver.Value) error {
	if r.sent {
		if r.mode == "rows" {
			return errors.New("row iteration failed")
		}
		return io.EOF
	}
	r.sent = true
	if r.mode == "scan" {
		copy(values, []driver.Value{"bad", "instance_id", "TEXT", int64(0), nil, int64(0)})
		return nil
	}
	copy(values, []driver.Value{int64(0), "workspace_id", "TEXT", int64(1), nil, int64(1)})
	return nil
}
