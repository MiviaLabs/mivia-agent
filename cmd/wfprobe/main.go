// Probe: read the workflow ledger for one run the same way the TUI dialog
// does (storage.OpenSQLite + ledger.NewStorageRepository + ListDeliveries),
// plus a raw dump of the wf_delivery_upserted events. Operates on a snapshot
// of the store taken with storage.SQLite.Backup (VACUUM INTO) into a fresh,
// private, per-invocation staging directory, so the probe never reads a torn
// copy of the live database and never leaves a readable store copy behind.
package main

import (
	"context"
	"database/sql"
	"fmt"
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

	tmp, cleanup, err := snapshotStore(context.Background(), srcDir)
	if err != nil {
		panic(err)
	}
	defer cleanup()

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

// snapshotStore snapshots the workflow store in srcDir into a fresh, private
// (0o700), per-invocation staging directory via storage.SQLite.Backup
// (VACUUM INTO) and returns the staging path plus a cleanup func that removes
// it. The backup reads the live store without an exclusive lock and captures
// committed durable state including uncheckpointed WAL, so the probe never
// operates on a torn copy. A missing store fails hard here, before any
// connection opens, so the probe can never create an empty store in srcDir or
// silently probe one. The cleanup func is safe to call on every branch.
func snapshotStore(ctx context.Context, srcDir string) (string, func(), error) {
	noop := func() {}
	src := filepath.Join(srcDir, "context.db")
	if _, err := os.Stat(src); err != nil {
		return "", noop, fmt.Errorf("workflow store %s not found: %w", src, err)
	}
	tmp, err := os.MkdirTemp("", "wfprobe-*")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	live, err := storage.OpenSQLite(src)
	if err != nil {
		cleanup()
		return "", noop, err
	}
	defer live.Close()
	if err := live.Backup(ctx, filepath.Join(tmp, "context.db")); err != nil {
		cleanup()
		return "", noop, err
	}
	return tmp, cleanup, nil
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
// store, through a read-only connection on the snapshot.
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
