package storage

import (
	"database/sql"
	"fmt"
)

// migrateAutomationRunsSchema creates internal/automation's durable run
// history table (D1 of docs/design/automations.md): automations.toml
// (per-scope, on disk) is the definitions' source of truth, this table is
// the runs' source of truth. Columns exactly as D1 specifies. No
// execution/scheduling writes this table yet (chunk 1 only creates it);
// later chunks add the fenced-claim and lifecycle writers.
//
// Split out of OpenSQLiteWithOptions (sqlite.go) rather than inlined there,
// matching this package's own precedent (migrateContextSchema in
// context_schema.go): a schema migration is a named, separately testable
// step, not more lines pushed into an already-large constructor.
func migrateAutomationRunsSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS automation_runs (
		id TEXT PRIMARY KEY,
		automation_id TEXT NOT NULL,
		origin TEXT NOT NULL,
		state TEXT NOT NULL,
		step_index INTEGER NOT NULL DEFAULT 0,
		step_count INTEGER NOT NULL DEFAULT 0,
		session_name TEXT NOT NULL DEFAULT '',
		worktree_path TEXT NOT NULL DEFAULT '',
		worktree_branch TEXT NOT NULL DEFAULT '',
		claim_token TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		ended_at TEXT,
		fail_kind TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return fmt.Errorf("create automation_runs: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS automation_runs_automation_id_idx ON automation_runs(automation_id)`); err != nil {
		return fmt.Errorf("create automation_runs index: %w", err)
	}
	return nil
}
