// Probe: read the workflow ledger for one run the same way the TUI dialog
// does (storage.OpenSQLite + ledger.NewStorageRepository + ListDeliveries),
// plus a raw dump of the wf_delivery_upserted events. Operates on a COPY of
// the store so the live database is never touched.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func main() {
	srcDir := os.Args[1]
	runID := os.Args[2]

	tmp := filepath.Join(os.TempDir(), "wfprobe")
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		panic(err)
	}
	for _, name := range []string{"context.db", "context.db-wal", "context.db-shm"} {
		src := filepath.Join(srcDir, name)
		if _, err := os.Stat(src); err == nil {
			if err := copyFile(src, filepath.Join(tmp, name)); err != nil {
				panic(err)
			}
			fmt.Printf("copied %s\n", name)
		}
	}

	store, err := storage.OpenSQLite(filepath.Join(tmp, "context.db"))
	if err != nil {
		panic(err)
	}
	defer store.Close()

	repo := workflowledger.NewStorageRepository(store)
	ctx := context.Background()

	run, err := repo.GetRun(ctx, runID)
	fmt.Printf("GetRun: status=%s workflow=%s err=%v\n", run.Status, run.WorkflowName, err)

	deliveries, err := repo.ListDeliveries(ctx, runID)
	fmt.Printf("ListDeliveries: n=%d err=%v\n", len(deliveries), err)
	for _, d := range deliveries {
		fmt.Printf("  delivery: status=%q mode=%q remoteID=%q url=%q commit=%q base=%q head=%q provider=%q updated=%v\n",
			d.Status, d.Mode, d.RemoteID, d.URL, d.CommitSHA, d.BaseRef, d.HeadRef, d.Provider, d.UpdatedAt)
	}

	// Raw event dump through the same SQLite driver (second read-only conn
	// on the copy; the repository keeps its own pool open).
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(tmp, "context.db")+"?mode=ro")
	if err != nil {
		panic(err)
	}
	defer raw.Close()

	// List every run id in the store (sanity: where did inv runs go?).
	idRows, err := raw.QueryContext(ctx, `SELECT DISTINCT run_id FROM events ORDER BY run_id`)
	if err != nil {
		panic(err)
	}
	defer idRows.Close()
	fmt.Println("distinct run_ids in store:")
	for idRows.Next() {
		var rid string
		if err := idRows.Scan(&rid); err != nil {
			panic(err)
		}
		fmt.Printf("  %s\n", rid)
	}

	// Search payloads for the run id token (in case the id differs).
	var hit int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE payload LIKE ?`, "%65fdcfc5%").Scan(&hit); err != nil {
		panic(err)
	}
	fmt.Printf("events mentioning 65fdcfc5 in payload: %d\n", hit)

	rows, err := raw.QueryContext(ctx,
		`SELECT id, sequence, kind, CAST(payload AS TEXT), created_at FROM events WHERE run_id = ? ORDER BY sequence`, runID)
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, payload, created string
		var seq int64
		if err := rows.Scan(&id, &seq, &kind, &payload, &created); err != nil {
			panic(err)
		}
		fmt.Printf("event: seq=%d kind=%s created=%s id=%s payload=%s\n", seq, kind, created, id, payload)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
}
