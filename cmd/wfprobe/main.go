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

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: wfprobe <store-dir> <run-id>")
		os.Exit(2)
	}
	srcDir, runID := os.Args[1], os.Args[2]

	tmp := filepath.Join(os.TempDir(), "wfprobe")
	if err := copyStore(srcDir, tmp); err != nil {
		panic(err)
	}

	store, err := storage.OpenSQLite(filepath.Join(tmp, "context.db"))
	if err != nil {
		panic(err)
	}
	defer store.Close()

	repo := workflowledger.NewStorageRepository(store)
	ctx := context.Background()
	probeRun(ctx, repo, runID)

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(tmp, "context.db")+"?mode=ro")
	if err != nil {
		panic(err)
	}
	defer raw.Close()
	dumpEvents(ctx, raw, runID)
}

// copyStore copies the store files into tmp so the live database is never
// opened for writing. It clears any previous probe copy first.
func copyStore(srcDir, tmp string) error {
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"context.db", "context.db-wal", "context.db-shm"} {
		src := filepath.Join(srcDir, name)
		if _, err := os.Stat(src); err == nil {
			if err := copyFile(src, filepath.Join(tmp, name)); err != nil {
				return err
			}
			fmt.Printf("copied %s\n", name)
		}
	}
	return nil
}

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

// probeRun prints the run snapshot and its durable delivery records, which
// is the exact data the TUI dialog renders.
func probeRun(ctx context.Context, repo workflowledger.Repository, runID string) {
	run, err := repo.GetRun(ctx, runID)
	fmt.Printf("GetRun: status=%s workflow=%s err=%v\n", run.Status, run.WorkflowName, err)

	deliveries, err := repo.ListDeliveries(ctx, runID)
	fmt.Printf("ListDeliveries: n=%d err=%v\n", len(deliveries), err)
	for _, d := range deliveries {
		fmt.Printf("  delivery: status=%q mode=%q remoteID=%q url=%q commit=%q base=%q head=%q provider=%q updated=%v\n",
			d.Status, d.Mode, d.RemoteID, d.URL, d.CommitSHA, d.BaseRef, d.HeadRef, d.Provider, d.UpdatedAt)
	}
}

// dumpEvents prints every event for one run plus the distinct run ids in the
// store, through a read-only connection on the copy.
func dumpEvents(ctx context.Context, raw *sql.DB, runID string) {
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
